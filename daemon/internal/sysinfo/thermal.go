package sysinfo

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ThermalBoot resume un arranque: la temperatura más alta y si el firmware limitó la CPU
// o vio subtensión. Se guarda en disco para poder revisar el calor de la guantera.
type ThermalBoot struct {
	BootID      string    `json:"boot_id"`
	Start       time.Time `json:"start"`
	MaxTempC    float64   `json:"max_temp_c"`
	Throttled   bool      `json:"throttled"`
	Undervolted bool      `json:"undervoltage"`
}

const (
	thermalSample  = 30 * time.Second
	thermalSave    = 10 * time.Minute // máximo entre escrituras si nada cambia
	thermalKeep    = 30
	flagUnderNow   = 1 << 0
	flagThrottled  = 1 << 2
	flagUnderBoot  = 1 << 16
	flagLimitBoots = 1<<17 | 1<<18 | 1<<19 // frecuencia limitada, limitada o límite térmico suave alguna vez
)

// Sustituibles en las pruebas.
var (
	tempPath     = "/sys/class/thermal/thermal_zone0/temp"
	bootIDPath   = "/proc/sys/kernel/random/boot_id"
	readThrottle = vcgencmdThrottled
)

// vcgencmdThrottled lee las banderas de alimentación y temperatura del firmware.
func vcgencmdThrottled() (uint32, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "vcgencmd", "get_throttled").Output()
	if err != nil {
		return 0, false
	}
	return parseThrottled(string(out))
}

// parseThrottled entiende "throttled=0x50005".
func parseThrottled(s string) (uint32, bool) {
	k, v, ok := strings.Cut(strings.TrimSpace(s), "=")
	if !ok || k != "throttled" {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(v), "0x"), 16, 32)
	return uint32(n), err == nil
}

func readTemp() float64 {
	b, err := os.ReadFile(tempPath)
	if err != nil {
		return 0
	}
	v, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return float64(v) / 1000
}

// Run muestrea mientras viva ctx. Con Fake no hace nada.
func (r *Reader) Run(ctx context.Context) {
	if r.Fake {
		return
	}
	r.loadHeartbeat()
	r.loadThermal()
	r.sample()
	r.beat()
	t := time.NewTicker(thermalSample)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sample()
			r.beat()
		}
	}
}

func (r *Reader) loadThermal() {
	id := ""
	if b, err := os.ReadFile(bootIDPath); err == nil {
		id = strings.TrimSpace(string(b))
	}
	var hist []ThermalBoot
	if r.ThermalFile != "" {
		if b, err := os.ReadFile(r.ThermalFile); err == nil {
			json.Unmarshal(b, &hist)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(hist); n > 0 && hist[n-1].BootID == id {
		// El daemon se reinició sin que lo hiciera la Pi: se sigue con el mismo arranque.
		r.cur, hist = hist[n-1], hist[:n-1]
	} else {
		r.cur = ThermalBoot{BootID: id, Start: time.Now()}
	}
	r.hist = hist
}

func (r *Reader) sample() {
	temp := readTemp()
	flags, ok := readThrottle()
	r.mu.Lock()
	changed := false
	if temp > r.cur.MaxTempC+0.9 {
		r.cur.MaxTempC, changed = temp, true
	}
	if ok {
		r.flags, r.flagsOK = flags, true
		if (flags&(flagThrottled|flagLimitBoots) != 0) && !r.cur.Throttled {
			r.cur.Throttled, changed = true, true
		}
		if (flags&(flagUnderNow|flagUnderBoot) != 0) && !r.cur.Undervolted {
			r.cur.Undervolted, changed = true, true
		}
	}
	if !changed && time.Since(r.lastSave) < thermalSave {
		r.mu.Unlock()
		return
	}
	r.lastSave = time.Now()
	out := append(append([]ThermalBoot{}, r.hist...), r.cur)
	r.mu.Unlock()
	if len(out) > thermalKeep {
		out = out[len(out)-thermalKeep:]
	}
	r.saveThermal(out)
}

// saveThermal escribe de forma atómica: un corte de corriente deja el fichero anterior.
func (r *Reader) saveThermal(h []ThermalBoot) {
	if r.ThermalFile == "" {
		return
	}
	b, err := json.Marshal(h)
	if err != nil {
		return
	}
	tmp := filepath.Join(filepath.Dir(r.ThermalFile), ".thermal.tmp")
	if f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600); err == nil {
		_, werr := f.Write(b)
		if serr := f.Sync(); werr == nil && serr == nil {
			f.Close()
			os.Rename(tmp, r.ThermalFile)
			return
		}
		f.Close()
	}
}

// thermalView devuelve lo que enseña Read: banderas actuales, máximo del arranque e historial.
func (r *Reader) thermalView(in *Info) {
	r.mu.Lock()
	defer r.mu.Unlock()
	in.Undervoltage = r.flagsOK && r.flags&flagUnderNow != 0
	in.UVSinceBoot = r.flagsOK && r.flags&flagUnderBoot != 0
	in.Throttled = r.flagsOK && r.flags&flagThrottled != 0
	in.ThrottledSinceBoot = r.flagsOK && r.flags&flagLimitBoots != 0
	in.PrevAlive = r.prev
	in.TempMaxC = r.cur.MaxTempC
	if in.TempC > in.TempMaxC {
		in.TempMaxC = in.TempC
	}
	if r.cur.BootID != "" || len(r.hist) > 0 {
		in.Thermal = append(append([]ThermalBoot{}, r.hist...), r.cur)
	}
}
