package bootcfg

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"teslapi/internal/auth"
	"teslapi/internal/config"
)

const sample = `# comentario
language = fr
home_detection = bluetooth
wifi_ssid = "Mi Casa"
wifi_password = secreta123
device_address = aa:bb:cc:dd:ee:01
smb_share = \\nas\tesla
smb_user = pepe
smb_password = clave-nas
portal_password = portal123
device_min_rssi =
cosa_rara = 1
force_wifi = 0
`

func TestParseAndApply(t *testing.T) {
	v := Parse([]byte("\xef\xbb\xbf" + sample))
	if v["wifi_ssid"] != "Mi Casa" || v["device_min_rssi"] != "" {
		t.Fatalf("%v", v)
	}
	c := config.Defaults()
	r, err := Apply(&c, v)
	if err != nil {
		t.Fatal(err)
	}
	if c.Language != "fr" || c.WiFi.SSID != "Mi Casa" || c.Charger.Address != "AA:BB:CC:DD:EE:01" ||
		c.SMB.Share != "//nas/tesla" || c.Charger.RSSIMin != -90 {
		t.Fatalf("%+v", c)
	}
	if !c.Setup.Done {
		t.Fatal("con Wi-Fi, casa y NAS el asistente queda hecho")
	}
	if r.PortalPassword != "portal123" || len(r.Unknown) != 1 || r.Unknown[0] != "cosa_rara" {
		t.Fatalf("%+v", r)
	}
}

func TestApplyRejectsBadValueWithoutChanges(t *testing.T) {
	c := config.Defaults()
	before := c
	_, err := Apply(&c, map[string]string{"wifi_ssid": "X", "device_min_rssi": "fuerte"})
	if err == nil || !strings.Contains(err.Error(), "device_min_rssi") || !reflect.DeepEqual(c, before) {
		t.Fatalf("err=%v", err)
	}
	if _, err := Apply(&c, map[string]string{"home_detection": "sonar"}); err == nil {
		t.Fatal("modo inválido")
	}
}

func TestScrubKeepsEverythingButSecrets(t *testing.T) {
	out := string(Scrub([]byte(sample), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)))
	for _, bad := range []string{"secreta123", "clave-nas", "portal123"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%s sigue en el fichero", bad)
		}
	}
	for _, keep := range []string{"wifi_ssid = \"Mi Casa\"", "# comentario", "smb_user = pepe", "wifi_password ="} {
		if !strings.Contains(out, keep) {
			t.Fatalf("falta %q en:\n%s", keep, out)
		}
	}
	// Reimportar el fichero ya limpio no cambia nada.
	if Fingerprint(Parse([]byte(out))) == Fingerprint(Parse([]byte(sample))) {
		t.Fatal("la huella debe cambiar al quitar las contraseñas")
	}
}

func TestImporterRunsOncePerChange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "teslapi.txt")
	os.WriteFile(file, []byte(sample), 0o644)
	store, _ := config.Open(filepath.Join(dir, "config.json"))
	im := Importer{Path: file, StateFile: filepath.Join(dir, "sum"), Store: store, HashPassword: auth.Hash,
		WriteBoot: func(p string, b []byte) error { return os.WriteFile(p, b, 0o644) }}

	r, done, err := im.Run()
	if err != nil || !done || len(r.Applied) == 0 {
		t.Fatalf("%v %v %+v", err, done, r)
	}
	c := store.Get()
	if c.WiFi.Password != "secreta123" || c.SMB.Password != "clave-nas" || !auth.Check("portal123", c.Portal.PasswordHash) {
		t.Fatalf("secretos no guardados: %+v", c)
	}
	if b, _ := os.ReadFile(file); strings.Contains(string(b), "secreta123") {
		t.Fatal("la contraseña debía borrarse del fichero")
	}
	// Se edita algo en el portal; reiniciar NO debe pisarlo con el fichero.
	store.Update(func(c *config.Config) { c.WiFi.SSID = "Cambiada" })
	if _, done, _ := im.Run(); done {
		t.Fatal("el fichero no ha cambiado: no se vuelve a importar")
	}
	if store.Get().WiFi.SSID != "Cambiada" {
		t.Fatal("se pisó lo editado en el portal")
	}
	// Un cambio real en el fichero sí se importa.
	b, _ := os.ReadFile(file)
	os.WriteFile(file, []byte(strings.Replace(string(b), "language = fr", "language = de", 1)), 0o644)
	if _, done, _ := im.Run(); !done || store.Get().Language != "de" {
		t.Fatal("el cambio del fichero debía importarse")
	}
}

func TestRescue(t *testing.T) {
	f := filepath.Join(t.TempDir(), "t.txt")
	os.WriteFile(f, []byte("force_wifi = 1\n"), 0o644)
	if !Rescue(f) || Rescue(f+"x") {
		t.Fatal("rescue")
	}
}

func TestApplyExtraDevicesAndSecondWiFi(t *testing.T) {
	c := config.Defaults()
	r, err := Apply(&c, map[string]string{
		"extra_devices":  "aa:bb:cc:dd:ee:01, Baliza Salon ; 11-22-33-44-55-66",
		"wifi2_ssid":     "casa",
		"wifi2_password": "clave-casa-1",
	})
	if err != nil || len(r.Unknown) != 0 {
		t.Fatalf("err=%v unknown=%v", err, r.Unknown)
	}
	want := []config.Device{{Address: "AA:BB:CC:DD:EE:01"}, {Name: "Baliza Salon"}, {Address: "11-22-33-44-55-66"}}
	if !reflect.DeepEqual(c.Charger.Extra, want) {
		t.Fatalf("extra: %+v", c.Charger.Extra)
	}
	if len(c.WiFi.Extra) != 1 || c.WiFi.Extra[0].SSID != "casa" || c.WiFi.Extra[0].Password != "clave-casa-1" {
		t.Fatalf("wifi2: %+v", c.WiFi.Extra)
	}
	if !Secrets["wifi2_password"] {
		t.Fatal("wifi2_password debe borrarse del fichero tras importarlo")
	}
}
