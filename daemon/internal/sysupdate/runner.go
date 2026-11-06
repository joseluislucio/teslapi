package sysupdate

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Runner ejecuta las órdenes del sistema. La real usa apt y mount; la falsa (modo
// desarrollo y pruebas) reproduce la salida real guardada en testdata.
type Runner interface {
	Output(ctx context.Context, name string, args ...string) (string, error)
	// Stream ejecuta enviando cada línea de salida a line y cada línea del descriptor de
	// estado de apt (-o APT::Status-Fd=3) a status.
	Stream(ctx context.Context, name string, args []string, line, status func(string)) error
}

// ExecRunner ejecuta de verdad. Outside lanza las órdenes como unidad aparte de systemd:
// apt, dpkg y los remontajes necesitan más de lo que el servicio del daemon tiene permitido.
type ExecRunner struct{ Outside bool }

// aptEnv: INITRD=No hace que los hooks del kernel no generen initramfs. Esta Pi arranca sin él
// (auto_initramfs=0) y /var/tmp es un tmpfs de 16 MB, donde mkinitramfs no cabe.
var aptEnv = []string{"DEBIAN_FRONTEND=noninteractive", "LC_ALL=C", "LANG=C", "NEEDRESTART_MODE=a", "INITRD=No"}

func env() []string { return append(os.Environ(), aptEnv...) }

// wrap devuelve la orden que hay que ejecutar. systemd-run no reenvía descriptores extra, así
// que el estado de apt (Status-Fd) sale por la salida normal; onePipe avisa de ello.
func (e ExecRunner) wrap(name string, args []string) (string, []string, bool) {
	if !e.Outside {
		return name, args, false
	}
	out := []string{"--wait", "--pipe", "--collect", "--quiet", "--service-type=exec"}
	for _, v := range aptEnv {
		out = append(out, "--setenv="+v)
	}
	out = append(out, "--", name)
	for _, a := range args {
		if a == "APT::Status-Fd=3" {
			a = "APT::Status-Fd=1"
		}
		out = append(out, a)
	}
	return "systemd-run", out, true
}

func (e ExecRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	bin, argv, _ := e.wrap(name, args)
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Env = env()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s: %w", name, err)
	}
	return out.String(), nil
}

func (e ExecRunner) Stream(ctx context.Context, name string, args []string, line, status func(string)) error {
	bin, argv, onePipe := e.wrap(name, args)
	if onePipe {
		// El estado llega mezclado con la salida: se separa aquí.
		orig := line
		line = func(l string) {
			if _, ok := parseStatus(l); ok {
				status(l)
				return
			}
			orig(l)
		}
	}
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Env = env()
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	sr, sw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr, cmd.ExtraFiles = pw, pw, []*os.File{sw} // el fd 3 es el de estado
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		sw.Close()
		sr.Close()
		return err
	}
	pw.Close()
	sw.Close()
	var wg sync.WaitGroup
	pump := func(f *os.File, fn func(string)) {
		defer wg.Done()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			if fn != nil {
				fn(sc.Text())
			}
		}
	}
	wg.Add(2)
	go pump(pr, line)
	go pump(sr, status)
	err = cmd.Wait()
	wg.Wait()
	pr.Close()
	sr.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

//go:embed testdata/sim.txt
var fakeSim string

//go:embed testdata/uris.txt
var fakeURIs string

// FakeRunner reproduce apt con datos reales: los mismos 27 paquetes pendientes que tenía
// la Pi. Instalar los quita de la lista, como haría apt de verdad.
type FakeRunner struct {
	// Speed acelera los tiempos (1 = tiempo "real" de demostración; 0 = instantáneo).
	Speed float64
	// FailOn hace fallar la orden cuya línea completa contenga el texto.
	FailOn string
	// Roots: estado de los remontajes (true = escritura).
	mu        sync.Mutex
	installed map[string]bool
	Calls     []string
	RW        map[string]bool
	Dpkg      string // salida de `dpkg --audit`
}

func (f *FakeRunner) sleep(sec float64) {
	if f.Speed > 0 {
		time.Sleep(time.Duration(sec / f.Speed * float64(time.Second)))
	}
}

func (f *FakeRunner) record(name string, args []string) string {
	l := name + " " + strings.Join(args, " ")
	f.mu.Lock()
	f.Calls = append(f.Calls, l)
	f.mu.Unlock()
	return l
}

func (f *FakeRunner) fail(l string) error {
	// FailOn admite varias órdenes separadas por «|».
	for _, k := range strings.Split(f.FailOn, "|") {
		if k != "" && strings.Contains(l, k) {
			return errors.New("simulated failure: " + k)
		}
	}
	return nil
}

// wanted devuelve los paquetes de la orden (lo que va tras --no-install-recommends o --only-upgrade).
func wanted(args []string) map[string]bool {
	w := map[string]bool{}
	for i, a := range args {
		if a == "install" {
			for _, p := range args[i+1:] {
				if !strings.HasPrefix(p, "-") {
					w[p] = true
				}
			}
		}
	}
	return w
}

func (f *FakeRunner) pending(only map[string]bool) (inst, uris []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range strings.Split(fakeSim, "\n") {
		if m := instRe.FindStringSubmatch(l); m != nil {
			n := strings.SplitN(m[1], ":", 2)[0]
			if !f.installed[n] && (len(only) == 0 || only[n]) {
				inst = append(inst, l)
			}
		}
	}
	for _, l := range strings.Split(fakeURIs, "\n") {
		if strings.HasPrefix(l, "'") {
			if fl := strings.Fields(l); len(fl) >= 3 {
				n := strings.SplitN(fl[1], "_", 2)[0]
				if !f.installed[n] && (len(only) == 0 || only[n]) {
					uris = append(uris, l)
				}
			}
		}
	}
	return
}

func (f *FakeRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	l := f.record(name, args)
	if err := f.fail(l); err != nil {
		return "", err
	}
	switch {
	case name == "findmnt":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.RW != nil && f.RW[args[len(args)-1]] {
			return "rw,relatime\n", nil
		}
		return "ro,relatime\n", nil
	case name == "mount":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.RW == nil {
			f.RW = map[string]bool{}
		}
		f.RW[args[len(args)-1]] = strings.Contains(strings.Join(args, " "), "remount,rw")
		return "", nil
	case name == "dpkg":
		return f.Dpkg, nil
	case name == "apt-get" && has(args, "update"):
		f.sleep(3)
		return "Fetched 27.7 MB in 16s (1,759 kB/s)\nReading package lists...\n", nil
	case name == "apt-get" && has(args, "-s") && has(args, "dist-upgrade"):
		inst, _ := f.pending(nil)
		return "Reading package lists...\n" + strings.Join(inst, "\n") + "\n", nil
	case name == "apt-get" && has(args, "-s"):
		inst, _ := f.pending(wanted(args))
		return strings.Join(inst, "\n") + "\n", nil
	case name == "apt-get" && has(args, "--print-uris"):
		_, uris := f.pending(wanted(args))
		var sum int64
		for _, u := range uris {
			fl := strings.Fields(u)
			var n int64
			fmt.Sscan(fl[2], &n)
			sum += n
		}
		return strings.Join(uris, "\n") + fmt.Sprintf("\nNeed to get %.1f MB of archives.\nAfter this operation, %.1f MB of additional disk space will be used.\n", float64(sum)/1e6, float64(sum)*0.5/1e6), nil
	case name == "apt-get" && has(args, "clean"):
		return "", nil
	}
	return "", nil
}

func has(a []string, v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}

func (f *FakeRunner) Stream(ctx context.Context, name string, args []string, line, status func(string)) error {
	l := f.record(name, args)
	if err := f.fail(l); err != nil {
		line("E: " + err.Error())
		return err
	}
	if name == "dpkg" {
		line("Setting up half-configured packages ...")
		return nil
	}
	want := wanted(args)
	inst, uris := f.pending(want)
	switch {
	case has(args, "-d"): // descarga
		for i := range uris {
			for _, p := range []float64{0, 50, 100} {
				status(fmt.Sprintf("dlstatus:%d:%.4f:Retrieving file %d of %d", i+1, (float64(i)+p/100)/float64(len(uris))*100, i+1, len(uris)))
				f.sleep(0.12)
			}
		}
		line(fmt.Sprintf("Fetched %d files", len(uris)))
	default: // instalación
		for i, it := range inst {
			m := instRe.FindStringSubmatch(it)
			n := strings.SplitN(m[1], ":", 2)[0]
			for _, p := range []float64{0, 40, 80} {
				status(fmt.Sprintf("pmstatus:%s:%.4f:Unpacking %s", n, (float64(i)+p/100)/float64(len(inst))*100, n))
				f.sleep(0.2)
			}
			line("Setting up " + n + " ...")
			f.mu.Lock()
			if f.installed == nil {
				f.installed = map[string]bool{}
			}
			f.installed[n] = true
			f.mu.Unlock()
		}
		status("pmstatus:dpkg-exec:100.0000:Running dpkg")
	}
	return nil
}
