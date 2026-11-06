package bootcfg

import (
	"os"
	"strings"
	"testing"

	"teslapi/internal/config"
)

// La plantilla que se copia a la tarjeta debe ser válida tal cual: ni un valor inventado
// ni una clave que el daemon no conozca.
func TestShippedTemplateIsValid(t *testing.T) {
	b, err := os.ReadFile("../../../system/boot/teslapi.txt")
	if err != nil {
		t.Fatal(err)
	}
	c := config.Defaults()
	r, err := Apply(&c, Parse(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Unknown) > 0 {
		t.Fatalf("claves desconocidas en la plantilla: %v", r.Unknown)
	}
	if c.Trigger != config.TriggerBLE || c.Setup.Done {
		t.Fatalf("la plantilla vacía no debe cambiar nada salvo el modo: %+v", c)
	}
	// Todas las claves que acepta Apply deberían estar documentadas en la plantilla.
	for _, k := range []string{"language", "wifi_ssid", "wifi_password", "wifi2_ssid", "wifi2_password", "home_detection", "device_address", "device_name", "extra_devices", "device_min_rssi",
		"smb_share", "smb_folder", "smb_user", "smb_password", "smb_domain", "smb_version", "smb_encrypt", "archive_recent_clips", "free_space_percent",
		"portal_password", "setup_ap_password", "force_wifi"} {
		if !strings.Contains(string(b), "\n"+k+" =") {
			t.Errorf("la plantilla no documenta %q", k)
		}
	}
}

// Valores con comillas, & y # (como los que volcará flash.py) llegan intactos.
func TestAwkwardWiFiValuesSurvive(t *testing.T) {
	b := []byte("wifi_ssid = Mi \"Casa\" & Co\nwifi_password = p@ss\\w$rd #1\n")
	c := config.Defaults()
	if _, err := Apply(&c, Parse(b)); err != nil {
		t.Fatal(err)
	}
	if c.WiFi.SSID != `Mi "Casa" & Co` || c.WiFi.Password != `p@ss\w$rd #1` {
		t.Fatalf("ssid=%q pass=%q", c.WiFi.SSID, c.WiFi.Password)
	}
}
