//go:build linux

package presence

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"teslapi/internal/shell"
)

// BlueZ escanea anuncios LE a través de bluetoothd (D-Bus del sistema).
type BlueZ struct {
	conn    *dbus.Conn
	adapter dbus.BusObject
	path    dbus.ObjectPath
}

func NewBlueZ(adapter string) (*BlueZ, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	// Pi OS arranca con el Bluetooth bloqueado por rfkill y BlueZ no puede encenderlo.
	shell.Run(context.Background(), "rfkill", "unblock", "bluetooth")
	p := dbus.ObjectPath("/org/bluez/" + adapter)
	b := &BlueZ{conn: conn, adapter: conn.Object("org.bluez", p), path: p}
	if err := b.adapter.SetProperty("org.bluez.Adapter1.Powered", dbus.MakeVariant(true)); err != nil {
		return nil, err
	}
	// DuplicateData: cada anuncio actualiza el RSSI, no solo el primero.
	filter := map[string]dbus.Variant{
		"Transport":     dbus.MakeVariant("le"),
		"DuplicateData": dbus.MakeVariant(true),
	}
	if err := b.adapter.Call("org.bluez.Adapter1.SetDiscoveryFilter", 0, filter).Err; err != nil {
		return nil, err
	}
	return b, nil
}

func (b *BlueZ) Scan(ctx context.Context, d time.Duration) ([]Sighting, error) {
	sig := make(chan *dbus.Signal, 64)
	b.conn.Signal(sig)
	defer b.conn.RemoveSignal(sig)
	match := []dbus.MatchOption{dbus.WithMatchSender("org.bluez")}
	if err := b.conn.AddMatchSignal(match...); err != nil {
		return nil, err
	}
	defer b.conn.RemoveMatchSignal(match...)

	if err := b.adapter.Call("org.bluez.Adapter1.StartDiscovery", 0).Err; err != nil {
		return nil, err
	}
	defer b.adapter.Call("org.bluez.Adapter1.StopDiscovery", 0)

	var mu sync.Mutex
	seen := map[dbus.ObjectPath]*Sighting{}
	note := func(p dbus.ObjectPath, props map[string]dbus.Variant) {
		if !strings.HasPrefix(string(p), string(b.path)+"/dev_") {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		s := seen[p]
		if s == nil {
			s = &Sighting{Address: addrFromPath(p), RSSI: -127}
		}
		if v, ok := props["RSSI"]; ok {
			if r, ok := v.Value().(int16); ok {
				s.RSSI, s.At = int(r), time.Now()
				seen[p] = s
			}
		}
		if v, ok := props["Name"]; ok {
			s.Name, _ = v.Value().(string)
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			break loop
		case s := <-sig:
			switch s.Name {
			case "org.freedesktop.DBus.ObjectManager.InterfacesAdded":
				if len(s.Body) == 2 {
					p, _ := s.Body[0].(dbus.ObjectPath)
					ifs, _ := s.Body[1].(map[string]map[string]dbus.Variant)
					if dev, ok := ifs["org.bluez.Device1"]; ok {
						note(p, dev)
					}
				}
			case "org.freedesktop.DBus.Properties.PropertiesChanged":
				if len(s.Body) >= 2 {
					if iface, _ := s.Body[0].(string); iface == "org.bluez.Device1" {
						props, _ := s.Body[1].(map[string]dbus.Variant)
						note(s.Path, props)
					}
				}
			}
		}
	}
	// Los dispositivos ya conocidos solo emiten RSSI: el nombre se completa de la caché de BlueZ.
	names := b.cachedNames()
	out := make([]Sighting, 0, len(seen))
	for p, s := range seen {
		if s.Name == "" {
			s.Name = names[p]
		}
		out = append(out, *s)
	}
	return out, nil
}

func (b *BlueZ) cachedNames() map[dbus.ObjectPath]string {
	objs := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{}
	names := map[dbus.ObjectPath]string{}
	err := b.conn.Object("org.bluez", "/").Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objs)
	if err != nil {
		return names
	}
	for p, ifs := range objs {
		if dev, ok := ifs["org.bluez.Device1"]; ok {
			if v, ok := dev["Alias"]; ok {
				names[p], _ = v.Value().(string)
			}
			if v, ok := dev["Name"]; ok {
				names[p], _ = v.Value().(string)
			}
		}
	}
	return names
}

func addrFromPath(p dbus.ObjectPath) string {
	s := string(p)
	i := strings.LastIndex(s, "dev_")
	if i < 0 {
		return ""
	}
	return strings.ReplaceAll(s[i+4:], "_", ":")
}
