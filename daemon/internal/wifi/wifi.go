// Package wifi enciende la Wi-Fi solo cuando hace falta (en casa, en el asistente o
// en modo rescate) y la deja bloqueada por rfkill el resto del tiempo.
package wifi

import (
	"context"
	"errors"
	"sort"
	"sync"
)

type Status struct {
	Enabled   bool `json:"enabled"`
	Connected bool `json:"connected"`
	// AP: la Pi está emitiendo su propia red (asistente de configuración).
	AP     bool   `json:"ap,omitempty"`
	SSID   string `json:"ssid,omitempty"`
	IP     string `json:"ip,omitempty"`
	Signal int    `json:"signal,omitempty"` // dBm
	Error  string `json:"error,omitempty"`
}

// Network es una red vista en un escaneo.
type Network struct {
	SSID     string `json:"ssid"`
	Strength int    `json:"strength"` // 0-100
	Secure   bool   `json:"secure"`
}

// HotspotIP es la dirección de la Pi en su propia red (la de NetworkManager en modo shared).
const HotspotIP = "10.42.0.1"

type Controller interface {
	// Up conecta ya a la red de casa y espera a tener IP.
	Up(ctx context.Context, ssid, password string) error
	// Standby deja la radio encendida y que se conecte sola cuando vea la red.
	Standby(ctx context.Context, ssid, password string) error
	Down(ctx context.Context) error
	Status(ctx context.Context) Status
	// Hotspot emite la red del asistente. Nunca abierta: por ella viaja la clave de casa.
	Hotspot(ctx context.Context, ssid, password string) error
	StopHotspot(ctx context.Context) error
	// Scan lista las redes a la vista (con el hotspot activo puede no ser posible).
	Scan(ctx context.Context) ([]Network, error)
}

// Tidy quita redes sin nombre y repetidas (se queda con la más fuerte) y ordena.
func Tidy(ns []Network) []Network {
	best := map[string]Network{}
	for _, n := range ns {
		if n.SSID == "" {
			continue
		}
		if b, ok := best[n.SSID]; !ok || n.Strength > b.Strength {
			best[n.SSID] = n
		}
	}
	out := make([]Network, 0, len(best))
	for _, n := range best {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Strength != out[j].Strength {
			return out[i].Strength > out[j].Strength
		}
		return out[i].SSID < out[j].SSID
	})
	return out
}

// Fake para desarrollo en el Mac y las pruebas.
type Fake struct {
	// Networks: lo que devuelve Scan.
	Networks []Network
	// Reject decide si una conexión falla (contraseña incorrecta, red fuera de alcance).
	Reject func(ssid, password string) error

	// LastHotspotPassword: lo último que se pasó a Hotspot.
	LastHotspotPassword string

	mu      sync.Mutex
	st      Status
	inRange bool
	standby bool
}

var (
	ErrNoNetwork = errors.New("network not found")
	// ErrOpenHotspot: se ha pedido emitir la red del asistente sin contraseña.
	ErrOpenHotspot = errors.New("refusing to start an open setup network")
)

func (f *Fake) Up(ctx context.Context, ssid, password string) error {
	if f.Reject != nil {
		if err := f.Reject(ssid, password); err != nil {
			f.mu.Lock()
			f.st = Status{Enabled: true}
			f.mu.Unlock()
			return err
		}
	}
	f.mu.Lock()
	f.st = Status{Enabled: true, Connected: true, SSID: ssid, IP: "192.168.1.77", Signal: -58}
	f.standby = false
	f.mu.Unlock()
	return nil
}

func (f *Fake) Standby(ctx context.Context, ssid, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.standby = true
	f.st = Status{Enabled: true, SSID: ssid}
	if f.inRange {
		f.st = Status{Enabled: true, Connected: true, SSID: ssid, IP: "192.168.1.77", Signal: -61}
	}
	return nil
}

// SetInRange simula llegar a casa (o irse) en el modo Wi-Fi.
func (f *Fake) SetInRange(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inRange = on
	if f.standby && f.st.Enabled {
		ssid := f.st.SSID
		f.st = Status{Enabled: true, SSID: ssid}
		if on {
			f.st = Status{Enabled: true, Connected: true, SSID: ssid, IP: "192.168.1.77", Signal: -61}
		}
	}
}

func (f *Fake) Down(context.Context) error {
	f.mu.Lock()
	f.st, f.standby = Status{}, false
	f.mu.Unlock()
	return nil
}

func (f *Fake) Status(context.Context) Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *Fake) Hotspot(ctx context.Context, ssid, password string) error {
	if password == "" {
		return ErrOpenHotspot
	}
	f.mu.Lock()
	f.LastHotspotPassword = password
	f.st, f.standby = Status{Enabled: true, AP: true, SSID: ssid, IP: HotspotIP}, false
	f.mu.Unlock()
	return nil
}

func (f *Fake) StopHotspot(context.Context) error {
	f.mu.Lock()
	if f.st.AP {
		f.st = Status{Enabled: true}
	}
	f.mu.Unlock()
	return nil
}

func (f *Fake) Scan(context.Context) ([]Network, error) {
	return Tidy(f.Networks), nil
}
