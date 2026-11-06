//go:build linux

package wifi

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"teslapi/internal/shell"
)

// Prueba con NetworkManager de verdad, en la Pi: TESLAPI_NM_TEST=<carpeta de perfiles>.
// La clave debe llegar al perfil sin pasar por la línea de órdenes.
func TestProfileWithPSKAgainstNetworkManager(t *testing.T) {
	dir := os.Getenv("TESLAPI_NM_TEST")
	if dir == "" {
		t.Skip("solo en la Pi: TESLAPI_NM_TEST=/data/state/nm")
	}
	ctx := context.Background()
	n := &NM{Iface: "wlan0", Dir: dir}
	const ssid, psk = "teslapi-prueba-sin-usar", `clave\raras #1;=x`
	profile := profileFor(ssid)
	defer shell.Run(ctx, "nmcli", "connection", "delete", profile)
	if err := n.ensureProfile(ctx, ssid, psk, false); err != nil {
		t.Fatal(err)
	}
	out, err := shell.Run(ctx, "nmcli", "-s", "-g", "802-11-wireless-security.key-mgmt,802-11-wireless-security.psk", "connection", "show", profile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(out), "\n")
	// nmcli -g escapa las barras en su salida.
	if len(got) != 2 || got[0] != "wpa-psk" || strings.ReplaceAll(got[1], `\\`, `\`) != psk {
		t.Fatalf("perfil: %q", got)
	}
	// Una segunda llamada con lo mismo no debe recrear nada.
	st, err := os.Stat(dir + "/" + profile + ".nmconnection")
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("permisos del perfil: %v", st.Mode())
	}
}

// La red del asistente de verdad, en la Pi: corta la Wi-Fi unos segundos y vuelve a
// TESLAPI_AP_TEST=<perfil de casa>. Debe emitir con WPA2 y negarse a hacerlo abierta.
func TestHotspotAgainstNetworkManager(t *testing.T) {
	home := os.Getenv("TESLAPI_AP_TEST")
	if home == "" {
		t.Skip("solo en la Pi: TESLAPI_AP_TEST=<perfil de casa>")
	}
	ctx := context.Background()
	n := &NM{Iface: "wlan0", Dir: "/data/state/nm"}
	defer shell.Run(ctx, "nmcli", "--wait", "40", "connection", "up", home)
	if err := n.Hotspot(ctx, "TeslaPi-PRUEBA", ""); !errors.Is(err, ErrOpenHotspot) {
		t.Fatalf("sin contraseña debe negarse: %v", err)
	}
	const pw = "prueba-red-asistente"
	if err := n.Hotspot(ctx, "TeslaPi-PRUEBA", pw); err != nil {
		t.Fatal(err)
	}
	defer n.StopHotspot(ctx)
	out, err := shell.Run(ctx, "nmcli", "-s", "-g", "802-11-wireless-security.key-mgmt,802-11-wireless-security.proto,802-11-wireless-security.pairwise,802-11-wireless-security.psk", "connection", "show", apProfile)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("perfil: %q", strings.ReplaceAll(strings.TrimSpace(out), pw, "<clave>"))
	if !strings.Contains(out, "wpa-psk") || !strings.Contains(out, "rsn") || !strings.Contains(out, "ccmp") || !strings.Contains(out, pw) {
		t.Fatalf("seguridad: %q", out)
	}
	info, _ := shell.Run(ctx, "iw", "dev", "wlan0", "info")
	if !strings.Contains(info, "type AP") {
		t.Fatalf("no emite: %s", info)
	}
	st := n.Status(ctx)
	if !st.AP || st.SSID != "TeslaPi-PRUEBA" {
		t.Fatalf("estado: %+v", st)
	}
	time.Sleep(40 * time.Second) // margen para verla desde otro equipo
	if err := n.StopHotspot(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/data/state/nm/" + apProfile + ".nmconnection"); err == nil {
		t.Fatal("el perfil con la contraseña debía borrarse")
	}
}
