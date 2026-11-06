package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDevicesAndHomeConfigured(t *testing.T) {
	c := Config{Trigger: TriggerBLE}
	if c.HomeConfigured() {
		t.Fatal("sin dispositivos no hay casa")
	}
	c.Charger.Extra = []Device{{Address: "11:22:33:44:55:66"}, {}}
	if !c.HomeConfigured() {
		t.Fatal("un dispositivo extra basta para configurar la casa")
	}
	if n := len(c.Charger.Devices()); n != 1 {
		t.Fatalf("los vacíos no cuentan: %d", n)
	}
	c.Charger.Name = "Beacon"
	if n := len(c.Charger.Devices()); n != 2 {
		t.Fatalf("principal + extra: %d", n)
	}
}

func TestWiFiNetworksAndHas(t *testing.T) {
	w := WiFi{SSID: "garaje", Password: "a", Extra: []WiFiNet{{SSID: "casa", Password: "b"}, {}}}
	if got := w.Networks(); len(got) != 2 || got[0].SSID != "garaje" || got[1].SSID != "casa" {
		t.Fatalf("orden: %+v", got)
	}
	if !w.Has("casa") || !w.Has("garaje") || w.Has("otra") || w.Has("") {
		t.Fatal("Has")
	}
}

func TestRedactAndMergeExtraSecrets(t *testing.T) {
	c := Config{WiFi: WiFi{SSID: "garaje", Password: "p1", Extra: []WiFiNet{{SSID: "casa", Password: "clave-casa"}}}}
	red := c.Redacted()
	if red.WiFi.Extra[0].Password != secretMask {
		t.Fatal("la contraseña de la red extra debe ir enmascarada")
	}
	if c.WiFi.Extra[0].Password != "clave-casa" {
		t.Fatal("Redacted no debe tocar la configuración viva")
	}
	back, err := red.MergeSecrets(c)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.WiFi, c.WiFi) {
		t.Fatalf("al devolver la máscara se conserva la contraseña: %+v", back.WiFi)
	}
}

// La máscara no sirve para mandar un secreto guardado a otro destino.
func TestMergeSecretsRefusesAnotherDestination(t *testing.T) {
	old := Config{SMB: SMB{Share: "//nas/tesla", User: "u", Password: "SECRETO"}, WiFi: WiFi{SSID: "Casa", Password: "PSK-casa-1"}}
	cases := map[string]func(*Config){
		"otro servidor":  func(c *Config) { c.SMB.Share = "//atacante/x" },
		"otro usuario":   func(c *Config) { c.SMB.User = "otro" },
		"otro dominio":   func(c *Config) { c.SMB.Domain = "OTRO" },
		"otra red":       func(c *Config) { c.WiFi.SSID = "RedAjena" },
		"otra red extra": func(c *Config) { c.WiFi.Extra = []WiFiNet{{SSID: "RedAjena", Password: secretMask}} },
	}
	for name, change := range cases {
		c := old.Redacted()
		change(&c)
		if m, err := c.MergeSecrets(old); err == nil {
			t.Errorf("%s: aceptado, smb=%q wifi=%q", name, m.SMB.Password, m.WiFi.Password)
		}
	}
	// Mismo servidor y otro recurso: es un cambio normal y conserva la contraseña.
	c := old.Redacted()
	c.SMB.Share = `\\NAS\otro`
	if m, err := c.MergeSecrets(old); err != nil || m.SMB.Password != "SECRETO" {
		t.Fatalf("mismo servidor: %v %q", err, m.SMB.Password)
	}
	// Una contraseña escrita de nuevo sí vale para otro destino.
	c = old.Redacted()
	c.SMB.Share, c.SMB.Password = "//otro/x", "nueva"
	if m, err := c.MergeSecrets(old); err != nil || m.SMB.Password != "nueva" {
		t.Fatalf("contraseña nueva: %v", err)
	}
}

func TestSMBValidate(t *testing.T) {
	bad := []SMB{
		{Subdir: "../state"}, {Subdir: "../../.."}, {Subdir: "/etc"}, {Subdir: "a/../../b"}, {Subdir: `..\\x`},
		{Version: "3.0,sec=none,ip=203.0.113.9"}, {Version: "1.0"},
		{User: "a\npassword=x"}, {Password: "a\nb"}, {Domain: "d\nx"},
		{Share: "nas/x"}, {Share: "//-o/x"}, {Share: "//nas"}, {Share: "//nas/x\ny"},
	}
	for _, s := range bad {
		if s.Validate() == nil {
			t.Errorf("aceptado: %+v", s)
		}
	}
	good := []SMB{{}, {Share: "//192.168.1.5/tesla", Subdir: "TeslaCam", Version: "3.1.1"}, {Share: `\\nas\Mis Videos`, Subdir: "a/b"}, {Share: "//nas.local/x", User: "dom\\u", Password: "p,a=ss#"}}
	for _, s := range good {
		if err := s.Validate(); err != nil {
			t.Errorf("rechazado %+v: %v", s, err)
		}
	}
}

// Sin contraseña del portal no hay instalación terminada, aunque el fichero lo diga.
func TestOpenWithoutPortalPasswordReopensSetup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"wifi":{"ssid":"s"},"smb":{"share":"//n/x"},"charger":{"address":"AA:BB:CC:DD:EE:FF"},"setup":{"done":true}}`), 0o600)
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Get().Setup.Done {
		t.Fatal("sin contraseña debe volver al asistente")
	}
	os.WriteFile(p, []byte(`{"setup":{"done":true},"portal":{"password_hash":"x"}}`), 0o600)
	if s, _ = Open(p); !s.Get().Setup.Done {
		t.Fatal("con contraseña sigue terminado")
	}
}
