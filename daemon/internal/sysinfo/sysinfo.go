// Package sysinfo lee el estado de la Pi: temperatura, memoria, alimentación y
// los tiempos de arranque que apunta teslapi-gadget.sh.
package sysinfo

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Info struct {
	TempC              float64       `json:"temp_c"`
	UptimeSec          float64       `json:"uptime_sec"`
	MemTotalMB         int           `json:"mem_total_mb"`
	MemAvailMB         int           `json:"mem_avail_mb"`
	Load1              float64       `json:"load1"`
	Undervoltage       bool          `json:"undervoltage"`  // ahora mismo
	UVSinceBoot        bool          `json:"uv_since_boot"` // en algún momento desde el arranque
	Throttled          bool          `json:"throttled"`
	ThrottledSinceBoot bool          `json:"throttled_since_boot"` // el firmware limitó la CPU en algún momento
	TempMaxC           float64       `json:"temp_max_c"`           // máxima de este arranque
	Thermal            []ThermalBoot `json:"thermal"`              // máxima y límites por arranque, el actual al final
	PrevAlive          *Alive        `json:"prev_alive"`           // cómo terminó el arranque anterior (corte de corriente)
	BootTimes          []float64     `json:"boot_times"`           // segundos hasta publicar el pendrive, más reciente al final
	DataFreeGB         float64       `json:"data_free_gb"`
	Version            string        `json:"version"`
}

type Reader struct {
	BootTimesFile string
	DataDir       string
	Version       string
	Fake          bool
	ThermalFile   string // historial térmico por arranque; vacío = no se guarda
	HeartbeatFile string // última señal de vida; vacío = no se guarda

	mu       sync.Mutex
	flags    uint32
	flagsOK  bool
	cur      ThermalBoot
	hist     []ThermalBoot
	lastSave time.Time
	prev     *Alive // última señal del arranque anterior
}

func (r *Reader) Read() Info {
	if r.Fake {
		return Info{TempC: 47.2, UptimeSec: 1834, MemTotalMB: 427, MemAvailMB: 301, Load1: 0.21,
			BootTimes: []float64{5.8, 5.4, 5.5, 5.3, 5.6, 5.2}, DataFreeGB: 14.6, Version: r.Version,
			PrevAlive: &Alive{BootID: "p", UptimeSec: 4740, At: time.Date(2026, 11, 4, 16, 14, 20, 0, time.UTC)},
			TempMaxC:  51.4, Thermal: []ThermalBoot{{BootID: "a", MaxTempC: 49.6}, {BootID: "b", MaxTempC: 53.1}, {BootID: "c", MaxTempC: 51.4}}}
	}
	in := Info{Version: r.Version}
	in.TempC = readTemp()
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		in.UptimeSec, _ = strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		in.Load1, _ = strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) < 2 {
				continue
			}
			kb, _ := strconv.Atoi(fs[1])
			switch fs[0] {
			case "MemTotal:":
				in.MemTotalMB = kb / 1024
			case "MemAvailable:":
				in.MemAvailMB = kb / 1024
			}
		}
		f.Close()
	}
	r.thermalView(&in)
	in.BootTimes = readBootTimes(r.BootTimesFile, 30)
	in.DataFreeGB = freeGB(r.DataDir)
	return in
}

func readBootTimes(p string, n int) []float64 {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var out []float64
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if v, err := strconv.ParseFloat(strings.TrimSpace(l), 64); err == nil {
			out = append(out, v)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// Uptime para el modo rescate y los logs.
func Uptime() time.Duration {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	return time.Duration(v * float64(time.Second))
}
