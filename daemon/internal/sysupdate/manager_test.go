package sysupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newMgr(t *testing.T) (*Manager, *FakeRunner) {
	t.Helper()
	f := &FakeRunner{Speed: 100}
	m := New(f, filepath.Join(t.TempDir(), "apt"), filepath.Join(t.TempDir(), "rates.json"))
	m.RebootFile = filepath.Join(t.TempDir(), "reboot-required")
	m.FreeBytes = func(string) uint64 { return 50e9 }
	return m, f
}

func check(t *testing.T, m *Manager) {
	t.Helper()
	if e, err := m.Check(); e != nil || err != nil {
		t.Fatalf("check: %v %v", e, err)
	}
	m.Wait()
	if s := m.Snapshot(); s.Error != nil || s.Plans[Software] == nil {
		t.Fatalf("check falló: %+v", s.Error)
	}
}

func TestCheckSplitsSoftwareAndFirmware(t *testing.T) {
	m, f := newMgr(t)
	check(t, m)
	s := m.Snapshot()
	sw, fw := s.Plans[Software], s.Plans[Firmware]
	if len(sw.Packages) != 26 || len(fw.Packages) != 1 || fw.Packages[0].Name != "rpi-eeprom" {
		t.Fatalf("software=%d firmware=%v", len(sw.Packages), fw.Packages)
	}
	if sw.ETASec <= 0 || fw.ETASec <= 0 || fw.DownloadBytes <= 0 || s.CheckedAt.IsZero() {
		t.Fatalf("ETA/tamaños: %+v %+v", sw, fw)
	}
	if !contains(fw.Notes, "reboot") {
		t.Fatal("el firmware debe avisar de reinicio")
	}
	// Todo lo que lee el sistema usa listas y .deb en /data, nunca en la raíz.
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "apt-get") && !strings.Contains(c, "Dir::State::lists="+m.CacheDir) {
			t.Fatalf("apt sin redirigir la caché: %s", c)
		}
	}
}

func TestRunFirmwareEndToEnd(t *testing.T) {
	m, f := newMgr(t)
	check(t, m)
	if e, err := m.Run(Firmware); e != nil || err != nil {
		t.Fatalf("run: %v %v", e, err)
	}
	var last float64
	var steps []string
	for {
		s := m.Snapshot()
		if s.Job != nil {
			if s.Job.Percent+0.001 < last {
				t.Fatalf("el progreso retrocede: %.1f → %.1f", last, s.Job.Percent)
			}
			last = s.Job.Percent
			if n := len(steps); n == 0 || steps[n-1] != s.Job.Step {
				steps = append(steps, s.Job.Step)
			}
			if s.Job.Done {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	m.Wait()
	s := m.Snapshot()
	if s.Job.Error != nil || s.Job.Applied != 1 || !s.Job.Reboot || s.Job.Percent != 100 || s.Job.Step != "done" {
		t.Fatalf("resultado: %+v", s.Job)
	}
	if got := strings.Join(steps, ">"); !strings.HasPrefix(got, "preflight>download>install") {
		t.Fatalf("pasos: %s", got)
	}
	// La raíz y /boot/firmware vuelven a solo lectura.
	if f.RW["/"] || f.RW["/boot/firmware"] {
		t.Fatalf("quedó en escritura: %v", f.RW)
	}
	// Se remontó antes de instalar y se sincronizó antes de volver a ro.
	var seq []string
	for _, c := range f.Calls {
		switch {
		case strings.HasPrefix(c, "mount -o remount,rw /"):
			seq = append(seq, "rw")
		case strings.HasPrefix(c, "apt-get") && strings.Contains(c, "Status-Fd=3") && !strings.Contains(c, " -d "):
			seq = append(seq, "install")
		case c == "sync ":
			seq = append(seq, "sync")
		case strings.HasPrefix(c, "mount -o remount,ro"):
			seq = append(seq, "ro")
		}
	}
	if got := strings.Join(seq, ","); !strings.HasPrefix(got, "rw,rw,install,sync,ro,ro") {
		t.Fatalf("orden de remontaje: %s", got)
	}
	// Lo aplicado deja de estar pendiente, y lo otro sigue igual.
	if n := len(s.Plans[Firmware].Packages); n != 0 {
		t.Fatalf("firmware pendiente tras aplicarlo: %d", n)
	}
	if n := len(s.Plans[Software].Packages); n != 26 {
		t.Fatalf("el software no debe cambiar: %d", n)
	}
	if s.Phase != "idle" {
		t.Fatal("debe quedar libre")
	}
}

func TestRunSoftwareDoesNotTouchBootFirmware(t *testing.T) {
	m, f := newMgr(t)
	check(t, m)
	m.Run(Software)
	m.Wait()
	for _, c := range f.Calls {
		if strings.Contains(c, "/boot/firmware") {
			t.Fatalf("el software no debe tocar /boot/firmware: %s", c)
		}
	}
	s := m.Snapshot()
	if s.Job.Error != nil || s.Job.Applied != 26 || len(s.Plans[Software].Packages) != 0 {
		t.Fatalf("%+v %d", s.Job, len(s.Plans[Software].Packages))
	}
}

func TestDownloadFailureLeavesSystemUntouched(t *testing.T) {
	m, f := newMgr(t)
	check(t, m)
	f.FailOn = " -d "
	m.Run(Software)
	m.Wait()
	s := m.Snapshot()
	if s.Job.Error == nil || s.Job.Error.Code != "update.download_failed" || !s.Job.Done {
		t.Fatalf("%+v", s.Job)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "mount -o remount,rw") {
			t.Fatalf("no debe remontar si falla la descarga: %s", c)
		}
	}
	if s.Phase != "idle" {
		t.Fatal("tras fallar debe poder reintentarse")
	}
}

func TestInstallFailureRestoresReadOnly(t *testing.T) {
	m, f := newMgr(t)
	check(t, m)
	f.FailOn = "Dpkg::Options::=--force-confold"
	m.Run(Firmware)
	m.Wait()
	s := m.Snapshot()
	if s.Job.Error == nil || s.Job.Error.Code != "update.install_failed" {
		t.Fatalf("%+v", s.Job.Error)
	}
	if f.RW["/"] || f.RW["/boot/firmware"] {
		t.Fatalf("tras fallar la instalación la raíz debe volver a solo lectura: %v", f.RW)
	}
}

func TestInterruptedDpkgIsRepairedFirst(t *testing.T) {
	m, f := newMgr(t)
	check(t, m)
	f.Dpkg = "The following packages are only half configured: curl"
	m.Run(Software)
	m.Wait()
	repair, dl := -1, -1
	for i, c := range f.Calls {
		if strings.HasPrefix(c, "dpkg --configure -a") {
			repair = i
		}
		if strings.Contains(c, " -d ") {
			dl = i
		}
	}
	if repair < 0 || dl < 0 || repair > dl {
		t.Fatalf("dpkg --configure -a debe ir antes de descargar: %d %d", repair, dl)
	}
}

func TestPreflightRefusals(t *testing.T) {
	m, _ := newMgr(t)
	m.Online = func() bool { return false }
	if e, _ := m.Check(); e == nil || e.Code != "update.no_network" {
		t.Fatalf("sin red: %v", e)
	}
	m.Online = nil
	m.Busy = func() string { return "archiving" }
	if e, _ := m.Check(); e == nil || e.Code != "update.busy" {
		t.Fatalf("ocupada: %v", e)
	}
	m.Busy = nil
	if e, _ := m.Run(Software); e == nil || e.Code != "update.no_plan" {
		t.Fatalf("sin comprobar: %v", e)
	}
	check(t, m)
	if e, _ := m.Run("otra"); e == nil || e.Code != "update.bad_group" {
		t.Fatalf("grupo: %v", e)
	}
}

func TestLowSpaceRefuses(t *testing.T) {
	m, f := newMgr(t)
	check(t, m)
	m.FreeBytes = func(p string) uint64 {
		if strings.HasSuffix(p, "apt") {
			return 1e6
		}
		return 50e9
	}
	m.Run(Software)
	m.Wait()
	s := m.Snapshot()
	if s.Job.Error == nil || s.Job.Error.Code != "update.low_space" {
		t.Fatalf("%+v", s.Job.Error)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, " -d ") {
			t.Fatal("sin sitio no se debe descargar")
		}
	}
}

func TestOneOperationAtATime(t *testing.T) {
	m, _ := newMgr(t)
	check(t, m)
	f := m.R.(*FakeRunner)
	f.Speed = 15 // lo bastante lento para solaparlo
	if e, err := m.Run(Software); e != nil || err != nil {
		t.Fatal(e, err)
	}
	if _, err := m.Run(Firmware); err != ErrBusy {
		t.Fatalf("segunda operación: %v", err)
	}
	if _, err := m.Check(); err != ErrBusy {
		t.Fatalf("comprobar durante la ejecución: %v", err)
	}
	m.Wait()
}

func TestNothingToDoAndRatesLearned(t *testing.T) {
	m, _ := newMgr(t)
	// Reloj falso: cada lectura avanza 10 s, así las duraciones medidas son medibles.
	var tick atomic.Int64
	m.Now = func() time.Time { return time.Unix(1_700_000_000+10*tick.Add(1), 0) }
	check(t, m)
	m.Run(Software)
	m.Wait()
	check(t, m)
	if e, _ := m.Run(Software); e == nil || e.Code != "update.nothing" {
		t.Fatalf("sin nada pendiente: %v", e)
	}
	// Las velocidades medidas se guardan y se recuperan en el siguiente arranque.
	if _, err := os.Stat(m.StateFile); err != nil {
		t.Fatalf("no se guardaron las velocidades: %v", err)
	}
	m2 := New(m.R, m.CacheDir, m.StateFile)
	if m2.rates == DefaultRates() {
		t.Fatal("debería haber aprendido")
	}
}

func TestETAShrinksAsItProgresses(t *testing.T) {
	m, f := newMgr(t)
	check(t, m)
	f.Speed = 8
	m.Run(Software)
	first := m.Snapshot().Job.ETASec
	var mid int
	for {
		s := m.Snapshot()
		if s.Job.Percent > 50 && mid == 0 {
			mid = s.Job.ETASec
		}
		if s.Job.Done {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	m.Wait()
	if first <= 0 || mid >= first {
		t.Fatalf("el ETA no baja: inicio=%d mitad=%d", first, mid)
	}
	if m.Snapshot().Job.ETASec != 0 {
		t.Fatal("al terminar el ETA es 0")
	}
}

// El portal distingue «al día» por una lista vacía: no puede llegar como null.
func TestEmptyPlansSerializeAsEmptyList(t *testing.T) {
	m, _ := newMgr(t)
	check(t, m)
	m.Run(Software)
	m.Wait()
	b, err := json.Marshal(m.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"packages":null`) {
		t.Fatalf("packages null en el JSON: %s", b)
	}
}

// Caso real (Pi, 2026-11-05): al volver a solo lectura, `mount` da «mount point is busy» si algún
// proceso tiene abierto un fichero ya sustituido. La instalación ha ido bien: hay que decirlo
// y pedir reinicio, no dar error ni dejarlo en silencio.
func TestRootThatCannotGoReadOnlyAsksForReboot(t *testing.T) {
	m, f := newMgr(t)
	m.Pause = func(time.Duration) {}
	check(t, m)
	f.FailOn = "remount,ro"
	m.Run(Software)
	m.Wait()
	s := m.Snapshot()
	if s.Job.Error != nil || s.Job.Applied != 26 {
		t.Fatalf("la instalación fue bien: %+v", s.Job)
	}
	if !s.Job.RootWritable || !s.Job.Reboot {
		t.Fatalf("debe avisar de raíz en escritura y pedir reinicio: %+v", s.Job)
	}
	tries := 0
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "mount -o remount,ro") {
			tries++
		}
	}
	if tries != 3 {
		t.Fatalf("reintentos: %d", tries)
	}
	if !strings.Contains(strings.Join(s.Job.Log, "\n"), "sigue en escritura") {
		t.Fatal("falta el aviso en el registro")
	}
}

func TestNormalRunLeavesRootReadOnlyFlagOff(t *testing.T) {
	m, _ := newMgr(t)
	check(t, m)
	m.Run(Software)
	m.Wait()
	if s := m.Snapshot(); s.Job.RootWritable {
		t.Fatal("no debe avisar si la raíz volvió a ro")
	}
}

func TestNewEmpiezaIdle(t *testing.T) {
	// Regresión: con fase vacía el portal deshabilitaba «Comprobar ahora» hasta la primera comprobación.
	m := New(nil, t.TempDir(), t.TempDir()+"/r.json")
	if s := m.Snapshot(); s.Phase != "idle" {
		t.Fatalf("fase inicial = %q, quiero idle", s.Phase)
	}
}

// Caso real (Pi, 2026-11-05): falla dpkg Y la raíz no vuelve a solo lectura. El mensaje no puede
// decir que volvió: debe distinguirlo y pedir reinicio.
func TestInstallFailureWithRootStillWritable(t *testing.T) {
	m, f := newMgr(t)
	m.Pause = func(time.Duration) {}
	check(t, m)
	f.FailOn = "Dpkg::Options::=--force-confold|remount,ro"
	m.Run(Software)
	m.Wait()
	s := m.Snapshot()
	if s.Job.Error == nil || s.Job.Error.Code != "update.install_failed_rw" || !s.Job.RootWritable {
		t.Fatalf("%+v", s.Job)
	}
}

// Caso real (Pi, 2026-11-05): tras reiniciar, el portal decía «Sin comprobar todavía» aunque se
// había comprobado e instalado minutos antes. Lo último debe sobrevivir al reinicio, y el
// «reinicia la Pi» no debe repetirse si el sistema ya arrancó después.
func TestLastCheckAndJobSurviveRestart(t *testing.T) {
	m, f := newMgr(t)
	m.StateFile = t.TempDir() + "/update_rates.json"
	check(t, m)
	m.Run(Firmware)
	m.Wait()
	before := m.Snapshot()
	if before.Job == nil || !before.Job.Done || before.CheckedAt.IsZero() {
		t.Fatalf("%+v", before)
	}

	// Mismo arranque: se restaura todo, incluido el aviso de reiniciar.
	n := New(f, m.CacheDir, m.StateFile)
	n.Boot = func() time.Time { return before.Job.EndedAt.Add(-time.Hour) }
	n.loadLast()
	s := n.Snapshot()
	if s.CheckedAt.IsZero() || len(s.Plans) != 2 || s.Job == nil || !s.Job.Reboot {
		t.Fatalf("debe restaurar la comprobación y el trabajo: %+v", s)
	}

	// Arrancó después de instalar: el reinicio ya se hizo.
	n2 := New(f, m.CacheDir, m.StateFile)
	n2.Boot = func() time.Time { return before.Job.EndedAt.Add(time.Minute) }
	n2.loadLast()
	if s := n2.Snapshot(); s.Job == nil || s.Job.Reboot || s.Job.RootWritable || s.CheckedAt.IsZero() {
		t.Fatalf("tras reiniciar no debe pedir reinicio: %+v", s.Job)
	}
}

// Fuera del aislamiento, apt va por systemd-run y su estado sale por la salida normal.
func TestExecRunnerOutsideWrapsAndMovesStatusFd(t *testing.T) {
	bin, argv, one := ExecRunner{Outside: true}.wrap("apt-get", []string{"-o", "APT::Status-Fd=3", "install"})
	got := strings.Join(argv, " ")
	if bin != "systemd-run" || !one || !strings.Contains(got, "--wait --pipe") || !strings.Contains(got, "--setenv=INITRD=No") ||
		!strings.HasSuffix(got, "-- apt-get -o APT::Status-Fd=1 install") {
		t.Fatalf("%s %s %v", bin, got, one)
	}
	bin, argv, one = ExecRunner{}.wrap("apt-get", []string{"-o", "APT::Status-Fd=3"})
	if bin != "apt-get" || one || argv[1] != "APT::Status-Fd=3" {
		t.Fatalf("sin Outside: %s %v %v", bin, argv, one)
	}
}
