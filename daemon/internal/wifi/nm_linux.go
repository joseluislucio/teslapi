//go:build linux

package wifi

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"teslapi/internal/shell"
)

// NM controla la Wi-Fi con NetworkManager. Los perfiles viven en /data/state/nm
// (ver system/NetworkManager/teslapi.conf), porque la raíz es de solo lectura.
type NM struct {
	Iface string
	// Dir: carpeta de perfiles de NetworkManager. Las claves se escriben ahí, no por la línea
	// de órdenes, que cualquier proceso puede leer en /proc.
	Dir string
}

const apProfile = "teslapi-setup"

// profileFor da a cada red su propio perfil de NetworkManager (hay varias redes de casa).
func profileFor(ssid string) string {
	h := sha1.Sum([]byte(ssid))
	return "teslapi-home-" + hex.EncodeToString(h[:4])
}

func (n *NM) radioOn(ctx context.Context) error {
	if _, err := shell.Run(ctx, "rfkill", "unblock", "wifi"); err != nil {
		return err
	}
	_, err := shell.Run(ctx, "nmcli", "radio", "wifi", "on")
	return err
}

func (n *NM) Up(ctx context.Context, ssid, password string) error {
	if err := n.radioOn(ctx); err != nil {
		return err
	}
	if err := n.ensureProfile(ctx, ssid, password, false); err != nil {
		return err
	}
	// La radio tarda un momento en estar lista tras el rfkill.
	var err error
	for i := 0; i < 3; i++ {
		_, err = shell.Run(ctx, "nmcli", "--wait", "40", "connection", "up", profileFor(ssid), "ifname", n.Iface)
		if err == nil || ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return err
}

// Standby deja que NetworkManager se conecte solo al ver la red (modo de casa por Wi-Fi).
func (n *NM) Standby(ctx context.Context, ssid, password string) error {
	if err := n.radioOn(ctx); err != nil {
		return err
	}
	return n.ensureProfile(ctx, ssid, password, true)
}

func (n *NM) ensureProfile(ctx context.Context, ssid, password string, autoconnect bool) error {
	profile := profileFor(ssid)
	auto := map[bool]string{true: "yes", false: "no"}[autoconnect]
	out, _ := shell.Run(ctx, "nmcli", "-g", "802-11-wireless.ssid,802-11-wireless-security.psk,connection.autoconnect,802-11-wireless.powersave,ipv6.method", "-s", "connection", "show", profile)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 5 && lines[0] == ssid && lines[1] == password && lines[2] == auto && lines[3] == "2" && lines[4] == "link-local" {
		return nil
	}
	shell.Run(ctx, "nmcli", "connection", "delete", profile)
	args := []string{"connection", "add", "type", "wifi", "con-name", profile, "ifname", n.Iface, "ssid", ssid,
		"connection.autoconnect", auto, "ipv6.method", "link-local", "wifi.powersave", "2"}
	if _, err := shell.Run(ctx, "nmcli", args...); err != nil {
		return err
	}
	if password == "" {
		return nil
	}
	return n.setPSK(ctx, profile, password, "")
}

// keyfileEscape aplica el escapado de los ficheros de claves de GLib.
func keyfileEscape(s string) string {
	s = strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s)
	if strings.HasPrefix(s, " ") {
		s = `\s` + s[1:]
	}
	return s
}

// setPSK añade la clave al perfil escribiéndola en su fichero y recarga NetworkManager.
// extra son líneas adicionales de la sección de seguridad.
func (n *NM) setPSK(ctx context.Context, profile, password, extra string) error {
	file := filepath.Join(n.Dir, profile+".nmconnection")
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString("\n[wifi-security]\nkey-mgmt=wpa-psk\n" + extra + "psk=" + keyfileEscape(password) + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	_, err = shell.Run(ctx, "nmcli", "connection", "reload")
	return err
}

func (n *NM) Down(ctx context.Context) error {
	shell.Run(ctx, "nmcli", "radio", "wifi", "off")
	_, err := shell.Run(ctx, "rfkill", "block", "wifi")
	return err
}

// Hotspot crea la red del asistente. NetworkManager en modo "shared" da IP por DHCP
// (dnsmasq) y system/NetworkManager/dnsmasq-shared.d hace que todo nombre resuelva a la
// Pi, para que el móvil abra el portal solo (portal cautivo).
func (n *NM) Hotspot(ctx context.Context, ssid, password string) error {
	if len(password) < 8 {
		return ErrOpenHotspot
	}
	if err := n.radioOn(ctx); err != nil {
		return err
	}
	shell.Run(ctx, "nmcli", "connection", "delete", apProfile)
	args := []string{"connection", "add", "type", "wifi", "con-name", apProfile, "ifname", n.Iface, "ssid", ssid,
		"connection.autoconnect", "no", "802-11-wireless.mode", "ap", "802-11-wireless.band", "bg",
		"ipv4.method", "shared", "ipv4.addresses", HotspotIP + "/24", "ipv6.method", "disabled"}
	if _, err := shell.Run(ctx, "nmcli", args...); err != nil {
		return err
	}
	if err := n.setPSK(ctx, apProfile, password, "proto=rsn\npairwise=ccmp\n"); err != nil {
		return errors.Join(errors.New("setup network password"), err)
	}
	_, err := shell.Run(ctx, "nmcli", "--wait", "30", "connection", "up", apProfile)
	return err
}

// StopHotspot apaga la red del asistente y borra su perfil, que lleva la contraseña.
func (n *NM) StopHotspot(ctx context.Context) error {
	_, err := shell.Run(ctx, "nmcli", "connection", "delete", apProfile)
	return err
}

func (n *NM) Scan(ctx context.Context) ([]Network, error) {
	if err := n.radioOn(ctx); err != nil {
		return nil, err
	}
	out, err := shell.Run(ctx, "nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY", "device", "wifi", "list", "ifname", n.Iface, "--rescan", "yes")
	if err != nil {
		return nil, err
	}
	return parseScan(out), nil
}

// parseScan lee la salida "terse" de nmcli: campos separados por ':' y los ':' del
// nombre escapados como '\:'.
func parseScan(out string) []Network {
	var ns []Network
	for _, l := range strings.Split(out, "\n") {
		f := splitTerse(l)
		if len(f) < 3 {
			continue
		}
		s, _ := strconv.Atoi(f[1])
		sec := strings.TrimSpace(f[2])
		ns = append(ns, Network{SSID: f[0], Strength: s, Secure: sec != "" && sec != "--"})
	}
	return Tidy(ns)
}

func splitTerse(l string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(l); i++ {
		switch {
		case l[i] == '\\' && i+1 < len(l):
			i++
			cur.WriteByte(l[i])
		case l[i] == ':':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(l[i])
		}
	}
	return append(out, cur.String())
}

func (n *NM) Status(ctx context.Context) Status {
	var st Status
	out, err := shell.Run(ctx, "nmcli", "-t", "-f", "WIFI", "general")
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Enabled = strings.TrimSpace(out) == "enabled"
	if !st.Enabled {
		return st
	}
	out, _ = shell.Run(ctx, "nmcli", "-t", "-f", "GENERAL.STATE,GENERAL.CONNECTION,IP4.ADDRESS", "device", "show", n.Iface)
	for _, l := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(l, ":")
		switch {
		case k == "GENERAL.STATE":
			st.Connected = strings.HasPrefix(v, "100")
		case k == "GENERAL.CONNECTION":
			st.AP = v == apProfile
		case strings.HasPrefix(k, "IP4.ADDRESS") && st.IP == "":
			st.IP, _, _ = strings.Cut(v, "/")
		}
	}
	if st.AP {
		// Emitiendo la red del asistente: no hay red de casa a la que estar conectada.
		st.Connected = false
		out, _ = shell.Run(ctx, "nmcli", "-g", "802-11-wireless.ssid", "connection", "show", apProfile)
		st.SSID = strings.TrimSpace(out)
		return st
	}
	out, _ = shell.Run(ctx, "iw", "dev", n.Iface, "link")
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if s, ok := strings.CutPrefix(l, "SSID: "); ok {
			st.SSID = s
		}
		if s, ok := strings.CutPrefix(l, "signal: "); ok {
			st.Signal, _ = strconv.Atoi(strings.Fields(s)[0])
		}
	}
	return st
}
