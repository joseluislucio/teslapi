// Package sdwear estima la vida útil de la tarjeta microSD a partir de lo que se escribe
// en ella. Sirve para cualquier tarjeta: la capacidad y la identidad salen de sysfs; si la
// tarjeta informa de su desgaste de verdad (eMMC o SD industrial con life_time) se usa
// esa lectura, y si no (lo normal en una microSD de consumo) se estima con los bytes
// escritos, acumulados en disco para que sobrevivan a los reinicios.
package sdwear

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Params son los supuestos de la estimación; los elige el usuario según su tarjeta.
type Params struct {
	PECycles int     // ciclos de borrado que aguanta la memoria
	WAF      float64 // amplificación de escritura (bytes que escribe la memoria por cada byte del host)
}

type DayCount struct {
	Date  string `json:"date"` // AAAA-MM-DD
	Bytes uint64 `json:"bytes"`
	Ops   uint64 `json:"ops"`
}

// Info es lo que ve el portal.
type Info struct {
	Present  bool   `json:"present"`
	Device   string `json:"device"`
	Vendor   string `json:"vendor"`
	Model    string `json:"model"`
	Date     string `json:"date"`
	Capacity uint64 `json:"capacity_bytes"`

	// Source: "reported" (lo dice la tarjeta) o "estimated" (por bytes escritos).
	Source      string `json:"source"`
	ReportedPct *int   `json:"reported_used_pct,omitempty"`
	EOL         string `json:"eol,omitempty"` // normal | warning | urgent

	PECycles       int     `json:"pe_cycles"`
	WAF            float64 `json:"waf"`
	EnduranceBytes uint64  `json:"endurance_bytes"`

	WrittenBytes uint64    `json:"written_bytes"`
	WriteOps     uint64    `json:"write_ops"`
	ReadBytes    uint64    `json:"read_bytes"`
	Since        time.Time `json:"tracked_since"`

	UsedPct      float64  `json:"used_pct"`
	RemainingPct float64  `json:"remaining_pct"`
	BytesPerDay  float64  `json:"bytes_per_day"`
	WriteIOPS    float64  `json:"write_iops"`    // media de escrituras por segundo en la ventana reciente
	AvgWriteKiB  float64  `json:"avg_write_kib"` // tamaño medio de cada escritura
	DaysLeft     *float64 `json:"days_left,omitempty"`
	HistoryDays  float64  `json:"history_days"`

	Days []DayCount `json:"days"`
}

type state struct {
	CardID string    `json:"card_id"`
	BootID string    `json:"boot_id"`
	Since  time.Time `json:"since"`

	// Últimos contadores leídos de sysfs en este arranque.
	LastWSect, LastWOps, LastRSect uint64

	TotalWSect, TotalWOps, TotalRSect uint64
	Days                              []DayCount `json:"days"`
}

type Reader struct {
	StateFile string
	Sys       string // raíz de sysfs (pruebas)
	MountInfo string // /proc/self/mountinfo (pruebas)
	BootIDF   string
	UptimeF   string // /proc/uptime (pruebas)
	Params    func() Params
	Fake      bool
	Now       func() time.Time

	mu  sync.Mutex
	st  state
	dev string
}

func (r *Reader) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
func (r *Reader) sys() string {
	if r.Sys != "" {
		return r.Sys
	}
	return "/sys"
}
func (r *Reader) mountinfo() string {
	if r.MountInfo != "" {
		return r.MountInfo
	}
	return "/proc/self/mountinfo"
}
func (r *Reader) bootID() string {
	p := r.BootIDF
	if p == "" {
		p = "/proc/sys/kernel/random/boot_id"
	}
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

func (r *Reader) uptime() time.Duration {
	p := r.UptimeF
	if p == "" {
		p = "/proc/uptime"
	}
	f := strings.Fields(readStr(p))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return time.Duration(v * float64(time.Second))
}

func readStr(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Device devuelve la tarjeta donde vive /data (o la primera mmcblk): el nombre del disco
// entero, sin la partición.
func (r *Reader) Device(mount string) string {
	if b, err := os.ReadFile(r.mountinfo()); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) < 5 || f[4] != mount {
				continue
			}
			// "... - fstype source options": la fuente va tras el guion.
			for i, x := range f {
				if x == "-" && i+2 < len(f) {
					if d := r.parentDisk(filepath.Base(f[i+2])); d != "" {
						return d
					}
				}
			}
		}
	}
	m, _ := filepath.Glob(filepath.Join(r.sys(), "block", "mmcblk*"))
	sort.Strings(m)
	for _, p := range m {
		if b := filepath.Base(p); !strings.Contains(b, "boot") && !strings.Contains(b, "rpmb") {
			return b
		}
	}
	return ""
}

// parentDisk pasa de mmcblk0p3 a mmcblk0 mirando sysfs (vale para sdX, nvmeXnYpZ, mmcblkXpY…).
func (r *Reader) parentDisk(name string) string {
	if _, err := os.Stat(filepath.Join(r.sys(), "block", name)); err == nil {
		return name
	}
	p, err := filepath.EvalSymlinks(filepath.Join(r.sys(), "class", "block", name))
	if err != nil {
		return ""
	}
	return filepath.Base(filepath.Dir(p))
}

var vendors = map[int]string{0x01: "Panasonic", 0x02: "Kioxia/Toshiba", 0x03: "SanDisk", 0x1b: "Samsung", 0x1d: "ADATA",
	0x27: "Phison", 0x28: "Lexar", 0x31: "Silicon Power", 0x41: "Kingston", 0x6f: "STMicroelectronics",
	0x74: "Transcend", 0x76: "Patriot", 0x82: "Sony", 0x9c: "Angelbird"}

func vendorName(manfid string) string {
	n, err := strconv.ParseInt(strings.TrimPrefix(manfid, "0x"), 16, 32)
	if err != nil {
		return ""
	}
	if v, ok := vendors[int(n)]; ok {
		return v
	}
	return fmt.Sprintf("0x%02x", n)
}

// reported lee el desgaste que informa la propia tarjeta (eMMC y algunas SD industriales).
func reported(devDir string) (pct *int, eol string) {
	if f := strings.Fields(readStr(filepath.Join(devDir, "life_time"))); len(f) > 0 {
		hi := -1
		for _, x := range f {
			if n, err := strconv.ParseInt(strings.TrimPrefix(x, "0x"), 16, 32); err == nil && int(n) > hi {
				hi = int(n)
			}
		}
		if hi >= 1 { // 1 = 0-10 % usado … 10 = 90-100 %, 11 = superado
			p := hi * 10
			if p > 100 {
				p = 100
			}
			pct = &p
		}
	}
	switch strings.TrimPrefix(readStr(filepath.Join(devDir, "pre_eol_info")), "0x") {
	case "01", "1":
		eol = "normal"
	case "02", "2":
		eol = "warning"
	case "03", "3":
		eol = "urgent"
	}
	return
}

// stat lee los contadores acumulados del disco desde el arranque: operaciones y sectores
// de escritura, y sectores leídos.
func (r *Reader) stat(dev string) (wOps, wSect, rSect uint64, ok bool) {
	f := strings.Fields(readStr(filepath.Join(r.sys(), "block", dev, "stat")))
	if len(f) < 7 {
		return 0, 0, 0, false
	}
	n := func(i int) uint64 { v, _ := strconv.ParseUint(f[i], 10, 64); return v }
	return n(4), n(6), n(2), true
}

// Load recupera el acumulado del disco; si la tarjeta es otra, empieza de cero.
func (r *Reader) Load() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dev = r.Device("/data")
	if r.StateFile == "" {
		return
	}
	var s state
	if b, err := os.ReadFile(r.StateFile); err == nil {
		json.Unmarshal(b, &s)
	}
	cid := readStr(filepath.Join(r.sys(), "block", r.dev, "device", "cid"))
	if s.CardID != cid || s.Since.IsZero() {
		// El contador del kernel ya trae lo escrito desde el arranque: la medición empieza ahí.
		s = state{CardID: cid, Since: r.now().Add(-r.uptime())}
	}
	r.st = s
}

// Sample suma lo escrito desde la última muestra. Tras un arranque nuevo los contadores
// de sysfs empiezan en cero, así que se cuenta todo lo que llevan.
func (r *Reader) Sample() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dev == "" {
		return
	}
	wOps, wSect, rSect, ok := r.stat(r.dev)
	if !ok {
		return
	}
	boot := r.bootID()
	if r.st.CardID == "" {
		r.st.CardID = readStr(filepath.Join(r.sys(), "block", r.dev, "device", "cid"))
	}
	if r.st.Since.IsZero() {
		r.st.Since = r.now().Add(-r.uptime())
	}
	delta := func(cur, last uint64) uint64 {
		if boot != r.st.BootID || cur < last {
			return cur
		}
		return cur - last
	}
	dS, dO, dR := delta(wSect, r.st.LastWSect), delta(wOps, r.st.LastWOps), delta(rSect, r.st.LastRSect)
	r.st.BootID, r.st.LastWSect, r.st.LastWOps, r.st.LastRSect = boot, wSect, wOps, rSect
	r.st.TotalWSect += dS
	r.st.TotalWOps += dO
	r.st.TotalRSect += dR
	if dS > 0 || dO > 0 {
		day := r.now().Format("2006-01-02")
		if n := len(r.st.Days); n > 0 && r.st.Days[n-1].Date == day {
			r.st.Days[n-1].Bytes += dS * 512
			r.st.Days[n-1].Ops += dO
		} else {
			r.st.Days = append(r.st.Days, DayCount{Date: day, Bytes: dS * 512, Ops: dO})
			if len(r.st.Days) > 400 {
				r.st.Days = r.st.Days[len(r.st.Days)-400:]
			}
		}
	}
	r.save()
}

func (r *Reader) save() {
	if r.StateFile == "" {
		return
	}
	b, err := json.Marshal(r.st)
	if err != nil {
		return
	}
	tmp := r.StateFile + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, r.StateFile)
	}
}

// Run toma muestras cada pocos minutos y una última al parar.
func (r *Reader) Run(done <-chan struct{}) {
	if r.Fake {
		return
	}
	r.Load()
	r.Sample()
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-done:
			r.Sample()
			return
		case <-t.C:
			r.Sample()
		}
	}
}

// Read calcula lo que se enseña en el portal con el acumulado actual.
func (r *Reader) Read() Info {
	p := Params{PECycles: 1000, WAF: 2}
	if r.Params != nil {
		p = r.Params()
	}
	if r.Fake {
		return fake(p)
	}
	r.Sample()
	r.mu.Lock()
	defer r.mu.Unlock()
	in := Info{Device: r.dev, PECycles: p.PECycles, WAF: p.WAF, Source: "estimated"}
	if r.dev == "" {
		return in
	}
	in.Present = true
	dd := filepath.Join(r.sys(), "block", r.dev, "device")
	in.Vendor = vendorName(readStr(filepath.Join(dd, "manfid")))
	in.Model = readStr(filepath.Join(dd, "name"))
	in.Date = readStr(filepath.Join(dd, "date"))
	sectors, _ := strconv.ParseUint(readStr(filepath.Join(r.sys(), "block", r.dev, "size")), 10, 64)
	in.Capacity = sectors * 512
	return fill(in, r.st, p, r.now(), reportedOf(dd))
}

type rep struct {
	pct *int
	eol string
}

func reportedOf(dd string) rep { p, e := reported(dd); return rep{p, e} }

// fill calcula los porcentajes y la proyección; es pura, para poder probarla.
func fill(in Info, s state, p Params, now time.Time, rp rep) Info {
	in.WrittenBytes = s.TotalWSect * 512
	in.WriteOps = s.TotalWOps
	in.ReadBytes = s.TotalRSect * 512
	in.Since = s.Since
	in.Days = append([]DayCount(nil), s.Days...)
	if p.PECycles < 1 {
		p.PECycles = 1
	}
	if p.WAF < 1 {
		p.WAF = 1
	}
	in.EnduranceBytes = uint64(float64(in.Capacity) * float64(p.PECycles) / p.WAF)
	if in.EnduranceBytes > 0 {
		in.UsedPct = float64(in.WrittenBytes) / float64(in.EnduranceBytes) * 100
	}
	if rp.pct != nil {
		in.Source, in.ReportedPct, in.EOL = "reported", rp.pct, rp.eol
		in.UsedPct = float64(*rp.pct)
	} else if rp.eol != "" {
		in.EOL = rp.eol
	}
	if in.UsedPct > 100 {
		in.UsedPct = 100
	}
	in.RemainingPct = 100 - in.UsedPct
	if in.WriteOps > 0 {
		in.AvgWriteKiB = float64(in.WrittenBytes) / float64(in.WriteOps) / 1024
	}

	// MinProjectionDays es el historial mínimo para mostrar «tiempo restante».
	const MinProjectionDays = 7

	// Ritmo reciente: los últimos 7 días con datos, dividido por lo que lleva medido.
	elapsed := now.Sub(s.Since).Hours() / 24
	if elapsed < 0 {
		elapsed = 0
	}
	in.HistoryDays = elapsed
	win := elapsed
	if win > 7 {
		win = 7
	}
	if win >= 0.25 { // al menos 6 h medidas
		cut := now.AddDate(0, 0, -7).Format("2006-01-02")
		var b, o uint64
		for _, d := range s.Days {
			if d.Date >= cut {
				b += d.Bytes
				o += d.Ops
			}
		}
		in.BytesPerDay = float64(b) / win
		in.WriteIOPS = float64(o) / (win * 86400)
		if in.BytesPerDay > 0 {
			var left float64
			if rp.pct != nil {
				// Con lectura real, se proyecta con el ritmo y el desgaste informado.
				if *rp.pct > 0 && in.WrittenBytes > 0 {
					total := float64(in.WrittenBytes) / (float64(*rp.pct) / 100)
					left = (total - float64(in.WrittenBytes)) / in.BytesPerDay
				}
			} else {
				left = float64(in.EnduranceBytes-minU(in.EnduranceBytes, in.WrittenBytes)) / in.BytesPerDay
			}
			// Con menos de una semana de historial el ritmo no representa el uso real (un banco
			// de pruebas, una actualización…): proyectar años desde ahí es dar un dato falso.
			if left > 0 && elapsed >= MinProjectionDays {
				in.DaysLeft = &left
			}
		}
	}
	return in
}

func minU(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func fake(p Params) Info {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	in := Info{Present: true, Device: "mmcblk0", Vendor: "SanDisk", Model: "SA128", Date: "09/2026", Capacity: 127_999_672_320, Source: "estimated",
		PECycles: p.PECycles, WAF: p.WAF}
	s := state{Since: now.AddDate(0, 0, -9), TotalWSect: 3_400_000_000 / 512 * 100, TotalWOps: 41_000_000}
	for i := 8; i >= 0; i-- {
		s.Days = append(s.Days, DayCount{Date: now.AddDate(0, 0, -i).Format("2006-01-02"), Bytes: uint64(30e9 + float64(i)*2e9), Ops: uint64(900_000 + i*30_000)})
	}
	return fill(in, s, p, now, rep{})
}
