package sysupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"teslapi/internal/msg"
)

// Job es una ejecución de «actualizar» y lo que lleva hecho.
type Job struct {
	Group     Group     `json:"group"`
	Step      string    `json:"step"` // preflight | repair | download | install | cleanup | done
	Detail    string    `json:"detail,omitempty"`
	Percent   float64   `json:"percent"`
	ETASec    int       `json:"eta_sec"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Log       []string  `json:"log"`
	Done      bool      `json:"done"`
	Reboot    bool      `json:"reboot_required"`
	Applied   int       `json:"applied"` // paquetes actualizados en esta ejecución
	// RootWritable: tras instalar, la raíz no ha podido volver a solo lectura (hay ficheros
	// reemplazados que otros procesos aún tienen abiertos). Se arregla reiniciando.
	RootWritable bool     `json:"root_writable"`
	Error        *msg.Msg `json:"error,omitempty"`

	dlPct, pmPct float64
	wDl          float64 // peso de la descarga en el total, según el plan
	estTotal     float64
}

type State struct {
	Phase     string          `json:"phase"` // idle | checking | running
	CheckedAt time.Time       `json:"checked_at"`
	Plans     map[Group]*Plan `json:"plans"`
	Job       *Job            `json:"job,omitempty"`
	Error     *msg.Msg        `json:"error,omitempty"`
	Online    bool            `json:"online"`
}

type Manager struct {
	R         Runner
	CacheDir  string // listas y .deb: en /data, porque la raíz es de solo lectura
	StateFile string // velocidades medidas en ejecuciones anteriores
	// Online dice si hay red. Busy devuelve un motivo (no vacío) si ahora no se puede
	// tocar el sistema (por ejemplo, está archivando). FreeBytes mide el espacio libre.
	Online     func() bool
	Busy       func() string
	FreeBytes  func(path string) uint64
	RebootFile string
	Now        func() time.Time
	Pause      func(time.Duration) // por defecto time.Sleep
	// Boot da la hora de arranque del sistema (por defecto /proc/uptime). Sirve para no seguir
	// pidiendo un reinicio que ya se hizo.
	Boot func() time.Time

	mu    sync.Mutex
	st    State
	rates Rates
	wg    sync.WaitGroup
}

func New(r Runner, cacheDir, stateFile string) *Manager {
	m := &Manager{R: r, CacheDir: cacheDir, StateFile: stateFile, RebootFile: "/var/run/reboot-required", rates: DefaultRates()}
	m.st.Plans = map[Group]*Plan{}
	m.st.Phase = "idle" // el portal deshabilita los botones si la fase no es idle
	m.loadRates()
	m.loadLast()
	return m
}

// saved es lo último que se comprobó y lo último que se instaló: sobrevive a los reinicios,
// que son justo lo que pide una actualización de kernel.
type saved struct {
	CheckedAt time.Time       `json:"checked_at"`
	Plans     map[Group]*Plan `json:"plans"`
	Job       *Job            `json:"job,omitempty"`
}

func (m *Manager) lastFile() string {
	if m.StateFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(m.StateFile), "update_last.json")
}

func (m *Manager) bootTime() time.Time {
	if m.Boot != nil {
		return m.Boot()
	}
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return time.Time{}
	}
	var up float64
	if _, err := fmt.Sscanf(string(b), "%f", &up); err != nil {
		return time.Time{}
	}
	return time.Now().Add(-time.Duration(up * float64(time.Second)))
}

func (m *Manager) loadLast() {
	f := m.lastFile()
	if f == "" {
		return
	}
	var s saved
	if b, err := os.ReadFile(f); err != nil || json.Unmarshal(b, &s) != nil || s.CheckedAt.IsZero() {
		return
	}
	if s.Plans != nil {
		m.st.Plans = s.Plans
	}
	m.st.CheckedAt = s.CheckedAt
	if j := s.Job; j != nil && j.Done {
		// Si el sistema arrancó después de instalar, el reinicio pedido ya se hizo.
		if b := m.bootTime(); !b.IsZero() && j.EndedAt.Before(b) {
			j.Reboot, j.RootWritable = false, false
		}
		m.st.Job = j
	}
}

// saveLast guarda la última comprobación y el último trabajo terminado. Llamar con m.mu tomado.
func (m *Manager) saveLast() {
	f := m.lastFile()
	if f == "" {
		return
	}
	s := saved{CheckedAt: m.st.CheckedAt, Plans: m.st.Plans}
	if j := m.st.Job; j != nil && j.Done {
		c := *j
		if len(c.Log) > 60 {
			c.Log = c.Log[len(c.Log)-60:]
		}
		s.Job = &c
	}
	if b, err := json.Marshal(s); err == nil {
		tmp := f + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			os.Rename(tmp, f)
		}
	}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) loadRates() {
	if m.StateFile == "" {
		return
	}
	var r Rates
	if b, err := os.ReadFile(m.StateFile); err == nil && json.Unmarshal(b, &r) == nil && r.DownBps > 0 && r.SecPerPkg > 0 {
		m.rates = r
	}
}

func (m *Manager) saveRates() {
	if m.StateFile == "" {
		return
	}
	if b, err := json.Marshal(m.rates); err == nil {
		tmp := m.StateFile + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			os.Rename(tmp, m.StateFile)
		}
	}
}

// Wait espera a que acabe lo que esté en marcha (para las pruebas).
func (m *Manager) Wait() { m.wg.Wait() }

// Snapshot es una copia del estado para el portal.
func (m *Manager) Snapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.st
	s.Plans = map[Group]*Plan{}
	for k, v := range m.st.Plans {
		p := *v
		s.Plans[k] = &p
	}
	if m.st.Job != nil {
		j := *m.st.Job
		j.Log = append([]string(nil), j.Log...)
		s.Job = &j
	}
	s.Online = m.Online == nil || m.Online()
	return s
}

func (m *Manager) apt(extra ...string) []string {
	a := []string{
		"-o", "Dir::State::lists=" + filepath.Join(m.CacheDir, "lists"),
		"-o", "Dir::Cache::archives=" + filepath.Join(m.CacheDir, "archives"),
		"-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=", "-o", "APT::Color=0",
	}
	return append(a, extra...)
}

func (m *Manager) mkdirs() error {
	for _, d := range []string{"lists/partial", "archives/partial"} {
		if err := os.MkdirAll(filepath.Join(m.CacheDir, d), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// preflight comprueba que ahora se puede trabajar; devuelve el error que el portal enseña.
func (m *Manager) preflight() *msg.Msg {
	if m.Online != nil && !m.Online() {
		e := msg.New("update.no_network")
		return &e
	}
	if m.Busy != nil {
		if why := m.Busy(); why != "" {
			e := msg.New("update.busy", "why", why)
			return &e
		}
	}
	return nil
}

var ErrBusy = errors.New("sysupdate: ya hay una operación en marcha")

func (m *Manager) begin(phase string) (*msg.Msg, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.st.Phase != "" && m.st.Phase != "idle" {
		return nil, ErrBusy
	}
	if e := m.preflight(); e != nil {
		return e, nil
	}
	m.st.Phase, m.st.Error = phase, nil
	return nil, nil
}

// Check actualiza las listas y calcula qué haría cada botón. Es asíncrono.
func (m *Manager) Check() (*msg.Msg, error) {
	if e, err := m.begin("checking"); e != nil || err != nil {
		return e, err
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		err := m.check(ctx, true)
		m.mu.Lock()
		m.st.Phase = "idle"
		if err != nil {
			m.st.Error = err
		}
		m.mu.Unlock()
	}()
	return nil, nil
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

// check calcula los dos planes; con refresh baja antes las listas.
func (m *Manager) check(ctx context.Context, refresh bool) *msg.Msg {
	if err := m.mkdirs(); err != nil {
		e := msg.New("update.check_failed").WithDetail(err)
		return &e
	}
	if refresh {
		if out, err := m.R.Output(ctx, "apt-get", m.apt("update")...); err != nil {
			e := msg.New("update.check_failed").WithDetail(errors.New(lastLines(out, 6)))
			return &e
		}
	}
	all, err := m.R.Output(ctx, "apt-get", m.apt("-s", "dist-upgrade")...)
	if err != nil {
		e := msg.New("update.check_failed").WithDetail(errors.New(lastLines(all, 6)))
		return &e
	}
	pkgs, _ := parseSim(all)
	by := map[Group][]string{}
	for _, p := range pkgs {
		by[Classify(p.Name)] = append(by[Classify(p.Name)], p.Name)
	}
	m.mu.Lock()
	rt := m.rates
	m.mu.Unlock()
	plans := map[Group]*Plan{}
	for _, g := range []Group{Software, Firmware} {
		p := Plan{Group: g, Packages: []Pkg{}}
		if names := by[g]; len(names) > 0 {
			// Se simula exactamente lo que se instalará: solo esos paquetes, con sus dependencias.
			args := append([]string{"install", "--only-upgrade", "--no-install-recommends"}, names...)
			sim, e1 := m.R.Output(ctx, "apt-get", m.apt(append([]string{"-s"}, args...)...)...)
			uris, e2 := m.R.Output(ctx, "apt-get", m.apt(append([]string{"-y", "--print-uris"}, args...)...)...)
			if e1 != nil || e2 != nil {
				e := msg.New("update.check_failed").WithDetail(errors.New(lastLines(sim+"\n"+uris, 6)))
				return &e
			}
			p = buildPlan(g, sim, uris, rt)
		}
		plans[g] = &p
	}
	m.mu.Lock()
	m.st.Plans, m.st.CheckedAt = plans, m.now()
	m.saveLast()
	m.mu.Unlock()
	return nil
}

// Run aplica un grupo. Descarga primero (sin tocar el sistema) y solo remonta la raíz en
// escritura durante la instalación.
func (m *Manager) Run(g Group) (*msg.Msg, error) {
	if g != Software && g != Firmware {
		e := msg.New("update.bad_group")
		return &e, nil
	}
	m.mu.Lock()
	plan := m.st.Plans[g]
	m.mu.Unlock()
	if plan == nil {
		e := msg.New("update.no_plan")
		return &e, nil
	}
	if len(plan.Packages) == 0 {
		e := msg.New("update.nothing")
		return &e, nil
	}
	if e, err := m.begin("running"); e != nil || err != nil {
		return e, err
	}
	plan2 := *plan
	job := &Job{Group: g, Step: "preflight", StartedAt: m.now(), ETASec: plan.ETASec}
	total := float64(plan.DownloadSec + plan.InstallSec)
	job.estTotal = total
	if total > 0 {
		job.wDl = float64(plan.DownloadSec) / total
	}
	m.mu.Lock()
	m.st.Job = job
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		err := m.run(ctx, job, plan2)
		m.mu.Lock()
		job.EndedAt, job.Done = m.now(), true
		if err != nil {
			job.Error = err
		} else {
			job.Step, job.Percent, job.ETASec = "done", 100, 0
		}
		m.st.Phase = "idle"
		m.saveLast()
		m.mu.Unlock()
	}()
	return nil, nil
}

func (m *Manager) logLine(j *Job, l string) {
	l = strings.TrimSpace(l)
	if l == "" {
		return
	}
	m.mu.Lock()
	j.Log = append(j.Log, l)
	if len(j.Log) > 200 {
		j.Log = j.Log[len(j.Log)-200:]
	}
	m.mu.Unlock()
}

func (m *Manager) setStep(j *Job, step string) {
	m.mu.Lock()
	j.Step, j.Detail = step, ""
	m.recalc(j)
	m.mu.Unlock()
}

// recalc rehace el porcentaje global y el tiempo restante (con m.mu tomado). Hasta tener
// un 8 % medido se usa la estimación del plan; después, el ritmo real de esta ejecución.
func (m *Manager) recalc(j *Job) {
	pct := j.wDl*j.dlPct + (1-j.wDl)*j.pmPct
	if pct > 99.5 && !j.Done {
		pct = 99.5
	}
	j.Percent = pct
	el := m.now().Sub(j.StartedAt).Seconds()
	total := j.estTotal
	if pct >= 8 && el > 0 {
		total = el / (pct / 100)
	}
	left := total - el
	if left < 0 {
		left = 0
	}
	j.ETASec = int(left + 0.5)
}

func (m *Manager) onStatus(j *Job) func(string) {
	return func(l string) {
		p, ok := parseStatus(l)
		if !ok {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		switch p.Kind {
		case "dlstatus":
			if p.Pct > j.dlPct {
				j.dlPct = p.Pct
			}
			j.Detail = p.Text
		case "pmstatus":
			if p.Pct > j.pmPct {
				j.pmPct = p.Pct
			}
			if p.Name != "dpkg-exec" {
				j.Detail = p.Name
			}
		}
		m.recalc(j)
	}
}

func fail(code string, kv ...any) *msg.Msg { e := msg.New(code, kv...); return &e }

func (m *Manager) run(ctx context.Context, j *Job, plan Plan) *msg.Msg {
	// 1. Comprobaciones previas: red, ocupación, espacio.
	if e := m.preflight(); e != nil {
		return e
	}
	if err := m.mkdirs(); err != nil {
		return failD("update.failed", err)
	}
	if m.FreeBytes != nil {
		if need := uint64(plan.DownloadBytes)*12/10 + 50e6; m.FreeBytes(m.CacheDir) < need {
			return fail("update.low_space", "need_mb", need/1e6, "where", "/data")
		}
		if need := uint64(max64(plan.DiskDelta, 0))*15/10 + 100e6; m.FreeBytes("/") < need {
			return fail("update.low_space", "need_mb", need/1e6, "where", "/")
		}
	}
	names := plan.names()
	inst := append([]string{"install", "--only-upgrade", "--no-install-recommends"}, names...)

	// 2. Una instalación anterior cortada (por ejemplo, sin corriente) se termina antes.
	if out, _ := m.R.Output(ctx, "dpkg", "--audit"); strings.TrimSpace(out) != "" {
		m.setStep(j, "repair")
		m.logLine(j, "dpkg --configure -a")
		if err := m.stream(ctx, j, "dpkg", []string{"--configure", "-a"}); err != nil {
			return failD("update.failed", err)
		}
	}

	// 3. Descarga: no toca el sistema, así que cortarla es inocuo.
	m.setStep(j, "download")
	t0 := m.now()
	args := m.apt(append([]string{"-y", "-d", "-o", "APT::Status-Fd=3"}, inst...)...)
	if err := m.stream(ctx, j, "apt-get", args); err != nil {
		return failD("update.download_failed", err)
	}
	dlSec := m.now().Sub(t0).Seconds()
	m.mu.Lock()
	j.dlPct = 100
	m.recalc(j)
	m.mu.Unlock()

	// 4. Instalación, con la raíz (y /boot/firmware si es firmware) en escritura solo lo necesario.
	m.setStep(j, "install")
	restore := m.remountRW(ctx, j, g2paths(plan.Group))
	rootOK := true
	t1 := m.now()
	args = m.apt(append([]string{"-y", "-o", "APT::Status-Fd=3", "-o", "Dpkg::Options::=--force-confold", "-o", "Dpkg::Options::=--force-confdef"}, inst...)...)
	err := m.stream(ctx, j, "apt-get", args)
	rootOK = restore()
	if err != nil {
		if !rootOK {
			m.mu.Lock()
			j.RootWritable = true
			m.mu.Unlock()
			return failD("update.install_failed_rw", err)
		}
		return failD("update.install_failed", err)
	}
	instSec := m.now().Sub(t1).Seconds()
	m.mu.Lock()
	j.pmPct, j.Applied = 100, len(names)
	m.recalc(j)
	m.mu.Unlock()

	// 5. Limpieza de los .deb y aprendizaje de las velocidades para el próximo ETA.
	m.setStep(j, "cleanup")
	m.R.Output(ctx, "apt-get", m.apt("clean")...)
	m.learn(plan, dlSec, instSec)
	_, statErr := os.Stat(m.RebootFile)
	m.mu.Lock()
	j.RootWritable = !rootOK
	j.Reboot = contains(plan.Notes, "reboot") || statErr == nil || !rootOK
	m.mu.Unlock()

	// 6. Lo que quede pendiente se recalcula sin volver a bajar las listas.
	if e := m.check(ctx, false); e != nil {
		m.logLine(j, "recheck: "+e.String())
	}
	return nil
}

func failD(code string, err error) *msg.Msg { e := msg.New(code).WithDetail(err); return &e }

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func g2paths(g Group) []string {
	if g == Firmware {
		return []string{"/", "/boot/firmware"}
	}
	return []string{"/"}
}

func (m *Manager) stream(ctx context.Context, j *Job, name string, args []string) error {
	var tail []string
	err := m.R.Stream(ctx, name, args, func(l string) {
		m.logLine(j, l)
		if t := strings.TrimSpace(l); t != "" {
			tail = append(tail, t)
			if len(tail) > 6 {
				tail = tail[1:]
			}
		}
	}, m.onStatus(j))
	if err != nil {
		return errors.New(strings.Join(tail, "\n"))
	}
	return nil
}

// remountRW pone en escritura los puntos de montaje que estén en solo lectura y devuelve
// la función que los deja como estaban (sincroniza antes). Esa función devuelve false si
// alguno no ha querido volver: pasa cuando una librería sustituida sigue abierta por un
// proceso en marcha, y solo se arregla reiniciando.
func (m *Manager) remountRW(ctx context.Context, j *Job, paths []string) func() bool {
	var changed []string
	for _, p := range paths {
		out, err := m.R.Output(ctx, "findmnt", "-no", "OPTIONS", p)
		if err != nil || !hasOpt(out, "ro") {
			continue
		}
		if _, err := m.R.Output(ctx, "mount", "-o", "remount,rw", p); err != nil {
			m.logLine(j, "no se pudo remontar "+p+" en escritura: "+err.Error())
			continue
		}
		changed = append(changed, p)
	}
	return func() bool {
		ok := true
		bg := context.Background()
		m.R.Output(bg, "sync")
		for i := len(changed) - 1; i >= 0; i-- {
			var err error
			for try := 0; try < 3; try++ {
				if _, err = m.R.Output(bg, "mount", "-o", "remount,ro", changed[i]); err == nil {
					break
				}
				m.R.Output(bg, "sync")
				m.sleep(2 * time.Second)
			}
			if err != nil {
				ok = false
				m.logLine(j, "ATENCIÓN: "+changed[i]+" sigue en escritura (hay ficheros reemplazados aún abiertos); reinicia para dejarla en solo lectura")
			}
		}
		return ok
	}
}

// sleep es la espera entre reintentos (se acorta en las pruebas).
func (m *Manager) sleep(d time.Duration) {
	if m.Pause != nil {
		m.Pause(d)
		return
	}
	time.Sleep(d)
}

func hasOpt(opts, o string) bool {
	for _, x := range strings.Split(strings.TrimSpace(opts), ",") {
		if x == o {
			return true
		}
	}
	return false
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// learn mezcla lo medido con lo anterior (mitad y mitad) para afinar los próximos tiempos.
func (m *Manager) learn(p Plan, dlSec, instSec float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dlSec >= 3 && p.DownloadBytes > 1e6 {
		m.rates.DownBps = (m.rates.DownBps + float64(p.DownloadBytes)/dlSec) / 2
	}
	if n := len(p.Packages); n > 0 && instSec >= 3 {
		fixed := 6.0
		if p.Group == Firmware {
			fixed += 30
		}
		per := (instSec - fixed) / float64(n)
		if per < 0.5 {
			per = 0.5
		}
		m.rates.SecPerPkg = (m.rates.SecPerPkg + per) / 2
	}
	m.saveRates()
}
