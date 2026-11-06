// Package gadget conecta y desconecta el "pendrive" del coche.
//
// El gadget lo crea al arrancar system/teslapi-gadget.sh (lo antes posible, sin
// esperar al daemon). Aquí solo se desconecta un momento para liberar espacio y se
// vuelve a conectar, y se lee el estado del enlace USB.
package gadget

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Status struct {
	Attached  bool   `json:"attached"`   // el LUN está publicado en el UDC
	HostState string `json:"host_state"` // "configured" = el coche lo ha enumerado
}

type Controller interface {
	Attach() error
	Detach() error
	Status() Status
}

// ConfigFS opera sobre /sys/kernel/config/usb_gadget/<name>.
type ConfigFS struct {
	Dir string // /sys/kernel/config/usb_gadget/teslapi
}

func (c *ConfigFS) udc() string {
	m, _ := filepath.Glob("/sys/class/udc/*")
	if len(m) == 0 {
		return ""
	}
	return filepath.Base(m[0])
}

func (c *ConfigFS) Attach() error {
	if c.Status().Attached {
		return nil
	}
	return os.WriteFile(filepath.Join(c.Dir, "UDC"), []byte(c.udc()), 0o644)
}

func (c *ConfigFS) Detach() error {
	if !c.Status().Attached {
		return nil
	}
	return os.WriteFile(filepath.Join(c.Dir, "UDC"), []byte("\n"), 0o644)
}

func (c *ConfigFS) Status() Status {
	b, _ := os.ReadFile(filepath.Join(c.Dir, "UDC"))
	st := Status{Attached: strings.TrimSpace(string(b)) != ""}
	if u := c.udc(); u != "" {
		s, _ := os.ReadFile(filepath.Join("/sys/class/udc", u, "state"))
		st.HostState = strings.TrimSpace(string(s))
	}
	return st
}

type Fake struct {
	mu       sync.Mutex
	detached bool
}

func (f *Fake) Attach() error { f.mu.Lock(); f.detached = false; f.mu.Unlock(); return nil }
func (f *Fake) Detach() error { f.mu.Lock(); f.detached = true; f.mu.Unlock(); return nil }
func (f *Fake) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detached {
		return Status{HostState: "not attached"}
	}
	return Status{Attached: true, HostState: "configured"}
}
