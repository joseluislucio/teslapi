package shell

import (
	"strings"
	"testing"
)

func TestRedactHidesValueNotKeyName(t *testing.T) {
	args := strings.Fields("connection add con-name x wifi-sec.key-mgmt wpa-psk wifi-sec.psk S3cret ipv4.method auto")
	got := strings.Join(redact(args), " ")
	if strings.Contains(got, "S3cret") {
		t.Fatalf("la clave sale en claro: %s", got)
	}
	if !strings.Contains(got, "wifi-sec.psk ***") || !strings.Contains(got, "ipv4.method auto") {
		t.Fatalf("redacción incorrecta: %s", got)
	}
}

func TestRunErrorDoesNotLeakPassword(t *testing.T) {
	_, err := Run(t.Context(), "false", "wifi-sec.psk", "S3cret")
	if err == nil || strings.Contains(err.Error(), "S3cret") {
		t.Fatalf("el error filtra la clave: %v", err)
	}
}

// Un valor que contiene "pass" o "psk" también es un valor, no un nombre.
func TestRedactDoesNotDecideByTheValue(t *testing.T) {
	for _, pw := range []string{"MiPassword2024", "pskcasa99", "Compass-Azul"} {
		got := strings.Join(redact([]string{"add", "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", pw, "ifname", "wlan0"}), " ")
		if strings.Contains(got, pw+" ") || !strings.Contains(got, "wifi-sec.psk *** ifname wlan0") {
			t.Fatalf("clave %q: %s", pw, got)
		}
	}
}
