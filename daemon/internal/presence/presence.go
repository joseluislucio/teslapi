// Package presence decide si un dispositivo fijo de casa (cargador, baliza…) está cerca a partir de
// escaneos BLE cortos y periódicos, con histéresis para no encender y apagar la
// Wi-Fi por una lectura suelta.
package presence

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"teslapi/internal/config"
)

type Sighting struct {
	Address string    `json:"address"`
	Name    string    `json:"name"`
	RSSI    int       `json:"rssi"`
	At      time.Time `json:"at"`
}

// Scanner hace una ventana de escaneo y devuelve lo visto en ella.
type Scanner interface {
	Scan(ctx context.Context, d time.Duration) ([]Sighting, error)
}

func Matches(c config.Charger, s Sighting) bool {
	if s.RSSI < c.RSSIMin {
		return false
	}
	for _, d := range c.Devices() {
		// Con dirección, solo cuenta la dirección: cualquiera puede anunciar un nombre.
		// El nombre es para los dispositivos con la dirección aleatoria.
		if d.Address != "" {
			if strings.EqualFold(d.Address, s.Address) {
				return true
			}
			continue
		}
		if d.Name != "" && s.Name != "" && strings.Contains(strings.ToLower(s.Name), strings.ToLower(d.Name)) {
			return true
		}
	}
	return false
}

// Detector aplica la histéresis: presente con SeenWindows avistamientos en las
// últimas 3 ventanas; ausente tras AbsentAfterSec sin verlo.
type Detector struct {
	recent   []bool
	present  bool
	lastSeen time.Time
	lastRSSI int
}

func (d *Detector) Window(c config.Charger, seen bool, rssi int, now time.Time) (changed bool) {
	d.recent = append(d.recent, seen)
	if len(d.recent) > 3 {
		d.recent = d.recent[len(d.recent)-3:]
	}
	if seen {
		d.lastSeen, d.lastRSSI = now, rssi
	}
	if !d.present {
		n := 0
		for _, s := range d.recent {
			if s {
				n++
			}
		}
		need := c.SeenWindows
		if need < 1 {
			need = 1
		}
		if n >= need {
			d.present = true
			return true
		}
		return false
	}
	if now.Sub(d.lastSeen) >= time.Duration(c.AbsentAfterSec)*time.Second {
		d.present = false
		d.recent = nil
		return true
	}
	return false
}

type State struct {
	Present  bool      `json:"present"`
	LastSeen time.Time `json:"last_seen"`
	RSSI     int       `json:"rssi"`
	Error    string    `json:"error,omitempty"`
}

// Monitor ejecuta el ciclo de escaneo y publica los cambios de presencia.
type Monitor struct {
	scanner Scanner
	cfg     func() config.Charger
	changes chan bool

	mu       sync.Mutex
	det      Detector
	state    State
	discover time.Time // hasta cuándo se guardan todos los dispositivos vistos
	seen     map[string]Sighting
	transfer bool // con la Wi-Fi transfiriendo se espacian los escaneos (chip compartido)
}

func NewMonitor(s Scanner, cfg func() config.Charger) *Monitor {
	return &Monitor{scanner: s, cfg: cfg, changes: make(chan bool, 4), seen: map[string]Sighting{}}
}

func (m *Monitor) Changes() <-chan bool { return m.changes }

func (m *Monitor) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Discover guarda durante d todos los dispositivos BLE vistos, para elegir el cargador en el portal.
func (m *Monitor) Discover(d time.Duration) {
	m.mu.Lock()
	m.discover = time.Now().Add(d)
	m.seen = map[string]Sighting{}
	m.mu.Unlock()
}

func (m *Monitor) Discovered() (active bool, out []Sighting) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.seen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RSSI > out[j].RSSI })
	return time.Now().Before(m.discover), out
}

func (m *Monitor) SetTransferring(on bool) {
	m.mu.Lock()
	m.transfer = on
	m.mu.Unlock()
}

func (m *Monitor) Run(ctx context.Context) {
	for {
		c := m.cfg()
		m.mu.Lock()
		discovering := time.Now().Before(m.discover)
		transfer := m.transfer
		m.mu.Unlock()
		on := time.Duration(c.ScanOnSec) * time.Second
		period := time.Duration(c.ScanPeriodSec) * time.Second
		if discovering {
			on, period = 5*time.Second, 5*time.Second
		} else if transfer {
			period *= 3
		}
		start := time.Now()
		sights, err := m.scanner.Scan(ctx, on)
		if ctx.Err() != nil {
			return
		}
		seen, best := false, -200
		for _, s := range sights {
			if Matches(c, s) {
				seen = true
				if s.RSSI > best {
					best = s.RSSI
				}
			}
		}
		now := time.Now()
		m.mu.Lock()
		if discovering {
			for _, s := range sights {
				m.seen[s.Address] = s
			}
		}
		changed := m.det.Window(c, seen, best, now)
		m.state = State{Present: m.det.present, LastSeen: m.det.lastSeen, RSSI: m.det.lastRSSI}
		if err != nil {
			m.state.Error = err.Error()
		}
		present := m.det.present
		m.mu.Unlock()
		if changed {
			select {
			case m.changes <- present:
			default:
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(start.Add(period))):
		}
	}
}

// Fake es el escáner del modo desarrollo: el portal enciende o apaga el "cargador".
type Fake struct {
	mu      sync.Mutex
	Charger *Sighting
	Others  []Sighting
}

func (f *Fake) Set(s *Sighting) {
	f.mu.Lock()
	f.Charger = s
	f.mu.Unlock()
}

func (f *Fake) Scan(ctx context.Context, d time.Duration) ([]Sighting, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(d):
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]Sighting(nil), f.Others...)
	if f.Charger != nil {
		s := *f.Charger
		s.At = time.Now()
		out = append(out, s)
	}
	return out, nil
}
