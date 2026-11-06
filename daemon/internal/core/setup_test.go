package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"teslapi/internal/config"
	"teslapi/internal/presence"
	"teslapi/internal/wifi"
)

func init() {
	SetupSwitchDelay = 0
	StatusTTL = 0
}

// Primer arranque sin nada: la Pi emite su red, una contraseña mala la devuelve a su
// red con el motivo, la buena la deja en la LAN y al terminar entra en modo normal.
func TestSetupHotspotJoinFailRetryAndFinish(t *testing.T) {
	d, ble, wf, _, _, _ := setup(t, 900<<20)
	d.Cfg.Set(config.Defaults()) // configuración vacía, asistente sin hacer
	wf.Networks = []wifi.Network{{SSID: "Garaje", Strength: 80, Secure: true}, {SSID: "Vecino", Strength: 30, Secure: true}}
	wf.Reject = func(ssid, pw string) error {
		if pw != "buena-clave" {
			return errors.New("Secrets were required, but not provided")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	waitFor(t, "red del asistente", 3*time.Second, func() bool { return wf.Status(ctx).AP })
	st := d.Setup(ctx)
	if st.Step != "wifi" || st.APSSID != "TeslaPi-TEST" || len(st.Networks) != 2 {
		t.Fatalf("estado inicial: %+v", st)
	}
	if len(wf.LastHotspotPassword) < 8 {
		t.Fatalf("la red del asistente nunca es abierta: %q", wf.LastHotspotPassword)
	}
	if wf.Status(ctx).SSID != "TeslaPi-TEST" {
		t.Fatalf("SSID emitido: %q", wf.Status(ctx).SSID)
	}
	if d.Status(ctx).Mode != "setup" {
		t.Fatal("modo esperado: setup")
	}

	if err := d.SetupWiFi("Garaje", "corta", "fr"); err == nil {
		t.Fatal("una contraseña WPA de menos de 8 caracteres no vale")
	}
	if err := d.SetupWiFi("Garaje", "mala-clave", "fr"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "vuelta a su red con el error", 5*time.Second, func() bool {
		s := d.Setup(ctx)
		return s.Step == "wifi" && s.Error != nil && wf.Status(ctx).AP
	})
	if e := d.Setup(ctx).Error; e.Code != "wifi.join_failed" || e.Args["ssid"] != "Garaje" || e.Detail == "" {
		t.Fatalf("error: %+v", e)
	}

	if err := d.SetupWiFi("Garaje", "buena-clave", "fr"); err != nil {
		t.Fatal(err)
	}
	if d.Cfg.Get().Language != "fr" {
		t.Fatal("el idioma elegido se guarda con la Wi-Fi")
	}
	waitFor(t, "en la LAN", 5*time.Second, func() bool { return d.Setup(ctx).Step == "lan" })
	if s := wf.Status(ctx); s.AP || !s.Connected || s.SSID != "Garaje" {
		t.Fatalf("wifi: %+v", s)
	}
	// Mientras el asistente siga abierto, la Wi-Fi no se apaga aunque no haya cargador.
	time.Sleep(300 * time.Millisecond)
	d.poke()
	time.Sleep(300 * time.Millisecond)
	if !wf.Status(ctx).Connected {
		t.Fatal("durante el asistente la Wi-Fi debe seguir conectada")
	}

	d.Cfg.Update(func(c *config.Config) {
		c.Charger = config.Charger{Address: "AA:BB:CC:DD:EE:01", RSSIMin: -90, SeenWindows: 1, AbsentAfterSec: 1, ScanOnSec: 1, ScanPeriodSec: 1}
		c.SMB.Share = "//nas/tesla"
	})
	if err := d.FinishSetup("", ""); err != nil {
		t.Fatal(err)
	}
	if d.Setup(ctx).Step != "done" || !d.Cfg.Get().Setup.Done || d.Cfg.Get().Language != "fr" {
		t.Fatal("el asistente debía quedar terminado")
	}
	if err := d.SetupWiFi("Otra", "12345678", "fr"); err == nil {
		t.Fatal("con el asistente terminado no se cambia la Wi-Fi por esta vía")
	}
	// Modo normal por Bluetooth y sin el cargador a la vista: la Wi-Fi se apaga.
	waitFor(t, "Wi-Fi apagada", 20*time.Second, func() bool { return !wf.Status(ctx).Enabled })
	ble.Set(&presence.Sighting{Address: "AA:BB:CC:DD:EE:01", RSSI: -60})
	waitFor(t, "archivado al volver", 25*time.Second, func() bool { return d.Status(ctx).Archive.Phase == "done" })
}

// Modo «Wi-Fi de casa»: la radio queda en espera; conectar a la red de casa es llegar.
func TestWiFiTriggerArchivesWhenHomeNetworkAppears(t *testing.T) {
	d, _, wf, _, _, nasDir := setup(t, 900<<20)
	d.Cfg.Update(func(c *config.Config) { c.Trigger = config.TriggerWiFi; c.Charger = config.Charger{} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	waitFor(t, "radio en espera", 3*time.Second, func() bool { s := wf.Status(ctx); return s.Enabled && !s.Connected })
	if m := d.Status(ctx).Mode; m != "away" {
		t.Fatalf("modo %q, esperado away", m)
	}
	wf.SetInRange(true)
	d.poke()
	waitFor(t, "archivado en casa", 10*time.Second, func() bool { return d.Status(ctx).Archive.Phase == "done" })
	if d.Status(ctx).Mode != "home" {
		t.Fatal("conectado a la red de casa = en casa")
	}
	if _, err := os.Stat(filepath.Join(nasDir, "TeslaCam/SavedClips/2025-03-01_10-00-00/2025-03-01_09-59-00-front.mp4")); err != nil {
		t.Fatal(err)
	}
	wf.SetInRange(false)
	d.poke()
	waitFor(t, "NAS desmontado al irse", 5*time.Second, func() bool { _, ok := d.NAS.Dir(); return !ok })
	if !wf.Status(ctx).Enabled {
		t.Fatal("en modo Wi-Fi la radio sigue en espera al irse")
	}
}

// Sin dispositivo de casa configurado no hay forma de saber cuándo encenderla: la
// Wi-Fi queda encendida para poder terminar la configuración desde el portal.
func TestNoHomeDeviceKeepsWiFiOn(t *testing.T) {
	d, _, wf, _, _, _ := setup(t, 900<<20)
	d.Cfg.Update(func(c *config.Config) {
		c.Charger = config.Charger{RSSIMin: -90, SeenWindows: 1, AbsentAfterSec: 1, ScanOnSec: 1, ScanPeriodSec: 1}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	waitFor(t, "Wi-Fi encendida", 3*time.Second, func() bool { return wf.Status(ctx).Connected })
	if d.Status(ctx).HomeSetup {
		t.Fatal("home_setup debía ser false")
	}
}

// Sin contraseña en teslapi.txt se genera una, se publica una vez y se retira al terminar.
func TestSetupNetworkPasswordIsGeneratedAndPublished(t *testing.T) {
	d, _, wf, _, _, _ := setup(t, 900<<20)
	d.Cfg.Set(config.Defaults())
	var ssid, note string
	var mu sync.Mutex
	d.PublishAP = func(s, p string) error { mu.Lock(); defer mu.Unlock(); ssid, note = s, p; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	waitFor(t, "red del asistente", 3*time.Second, func() bool { return wf.Status(ctx).AP })
	mu.Lock()
	if ssid != "TeslaPi-TEST" || note != wf.LastHotspotPassword || len(note) < 16 {
		t.Fatalf("publicado %q/%q, emitido %q", ssid, note, wf.LastHotspotPassword)
	}
	mu.Unlock()
	if d.Cfg.Get().Setup.APPassword != note {
		t.Fatal("debe sobrevivir a un reinicio")
	}
	if err := d.FinishSetup("", "hash"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if note != "" || d.Cfg.Get().Setup.APPassword != "" {
		t.Fatal("al terminar se retira")
	}
}

// Sin nadie usando el asistente, su red se apaga y no vuelve hasta reiniciar.
func TestSetupNetworkTurnsOffWhenIdle(t *testing.T) {
	old := SetupAPWindow
	SetupAPWindow = 200 * time.Millisecond
	defer func() { SetupAPWindow = old }()
	d, _, wf, _, _, _ := setup(t, 900<<20)
	d.Cfg.Set(config.Defaults())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	waitFor(t, "red del asistente", 3*time.Second, func() bool { return wf.Status(ctx).AP })
	time.Sleep(300 * time.Millisecond)
	d.poke()
	waitFor(t, "red apagada", 3*time.Second, func() bool { return !wf.Status(ctx).AP && !wf.Status(ctx).Enabled })
	d.poke()
	time.Sleep(200 * time.Millisecond)
	if wf.Status(ctx).AP {
		t.Fatal("no debe volver hasta reiniciar")
	}
}
