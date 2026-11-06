package presence

import (
	"testing"
	"time"

	"teslapi/internal/config"
)

var cfg = config.Charger{Address: "AA:BB:CC:DD:EE:FF", RSSIMin: -85, SeenWindows: 2, AbsentAfterSec: 180}

func TestMatches(t *testing.T) {
	cases := []struct {
		c    config.Charger
		s    Sighting
		want bool
	}{
		{cfg, Sighting{Address: "aa:bb:cc:dd:ee:ff", RSSI: -60}, true},
		{cfg, Sighting{Address: "AA:BB:CC:DD:EE:FF", RSSI: -95}, false}, // demasiado lejos
		{cfg, Sighting{Address: "11:22:33:44:55:66", RSSI: -40}, false},
		{config.Charger{Name: "beacon", RSSIMin: -90}, Sighting{Address: "x", Name: "Home-Beacon-1234", RSSI: -70}, true},
		{config.Charger{RSSIMin: -90}, Sighting{Address: "x", Name: "", RSSI: -70}, false}, // sin configurar no casa nada
		// Con dirección configurada, anunciar el mismo nombre desde otra dirección no cuenta.
		{config.Charger{Address: "AA:BB:CC:DD:EE:FF", Name: "beacon", RSSIMin: -90}, Sighting{Address: "11:22:33:44:55:66", Name: "Home-Beacon-1234", RSSI: -70}, false},
	}
	for i, c := range cases {
		if got := Matches(c.c, c.s); got != c.want {
			t.Errorf("caso %d: got %v", i, got)
		}
	}
}

func TestHysteresis(t *testing.T) {
	var d Detector
	now := time.Date(2026, 10, 30, 20, 0, 0, 0, time.UTC)
	step := func(seen bool) bool {
		now = now.Add(10 * time.Second)
		return d.Window(cfg, seen, -60, now)
	}
	if step(true) || d.present {
		t.Fatal("un solo avistamiento no basta")
	}
	if step(false) {
		t.Fatal("no cambia")
	}
	if !step(true) || !d.present {
		t.Fatal("2 de 3 ventanas: presente")
	}
	// Huecos cortos (lecturas perdidas) no lo dan por ausente.
	for i := 0; i < 17; i++ {
		if step(false) {
			t.Fatalf("ausente demasiado pronto (ventana %d)", i)
		}
	}
	if !step(false) || d.present {
		t.Fatal("tras 180 s sin verlo: ausente")
	}
	// Al volver hay que reunir otra vez 2 de 3.
	if step(true) || d.present {
		t.Fatal("no debe reaparecer con un único avistamiento")
	}
}

func TestMatchesExtraDevice(t *testing.T) {
	c := config.Charger{Address: "AA:BB:CC:DD:EE:FF", RSSIMin: -85,
		Extra: []config.Device{{Address: "11:22:33:44:55:66"}, {Name: "baliza salon"}}}
	cases := []struct {
		s    Sighting
		want bool
	}{
		{Sighting{Address: "aa:bb:cc:dd:ee:ff", RSSI: -60}, true},                // el principal sigue valiendo
		{Sighting{Address: "11:22:33:44:55:66", RSSI: -60}, true},                // extra por dirección
		{Sighting{Address: "x", Name: "Baliza Salon 2", RSSI: -60}, true},        // extra por nombre
		{Sighting{Address: "11:22:33:44:55:66", RSSI: -95}, false},               // extra demasiado lejos
		{Sighting{Address: "99:99:99:99:99:99", Name: "otra", RSSI: -40}, false}, // ajeno
	}
	for i, c2 := range cases {
		if got := Matches(c, c2.s); got != c2.want {
			t.Errorf("caso %d: got %v", i, got)
		}
	}
	// Solo con un dispositivo extra (sin principal) también hay "casa".
	only := config.Charger{RSSIMin: -90, Extra: []config.Device{{Address: "11:22:33:44:55:66"}}}
	if !Matches(only, Sighting{Address: "11:22:33:44:55:66", RSSI: -50}) {
		t.Error("extra sin principal debe casar")
	}
}
