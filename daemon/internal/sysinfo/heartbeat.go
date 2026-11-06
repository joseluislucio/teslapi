package sysinfo

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// Alive es la última señal de vida de un arranque. Se reescribe con cada muestra, así
// que tras un corte de corriente dice cuánto duró ese arranque (UptimeSec no depende
// del reloj; la Pi no tiene RTC y At solo vale si hubo NTP) y a qué hora fue lo último.
type Alive struct {
	BootID    string    `json:"boot_id"`
	UptimeSec float64   `json:"uptime_sec"`
	At        time.Time `json:"at"`
	// Prev lleva la señal del arranque anterior en cada latido: si el servicio se reinicia
	// dentro del mismo arranque (un despliegue), el dato no se pierde.
	Prev *Alive `json:"prev,omitempty"`
}

// loadHeartbeat recoge la señal que dejó el arranque anterior, antes de pisarla.
func (r *Reader) loadHeartbeat() {
	if r.HeartbeatFile == "" {
		return
	}
	b, err := os.ReadFile(r.HeartbeatFile)
	if err != nil {
		return
	}
	var a Alive
	if json.Unmarshal(b, &a) != nil || a.BootID == "" {
		return
	}
	if a.BootID == readBootID() {
		// Mismo arranque (el servicio se reinició): se hereda lo que ya sabía.
		r.mu.Lock()
		r.prev = a.Prev
		r.mu.Unlock()
		return
	}
	a.Prev = nil
	r.mu.Lock()
	r.prev = &a
	r.mu.Unlock()
}

// beat anota que este arranque sigue vivo; el rename deja siempre un fichero entero.
func (r *Reader) beat() {
	if r.HeartbeatFile == "" {
		return
	}
	r.mu.Lock()
	prev := r.prev
	r.mu.Unlock()
	b, err := json.Marshal(Alive{BootID: readBootID(), UptimeSec: Uptime().Seconds(), At: time.Now(), Prev: prev})
	if err != nil {
		return
	}
	tmp := r.HeartbeatFile + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, r.HeartbeatFile)
	}
}

func readBootID() string {
	b, err := os.ReadFile(bootIDPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
