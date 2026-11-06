// Package bootcfg lee teslapi.txt, el fichero de configuración opcional de la
// partición de arranque (FAT, se edita desde cualquier ordenador).
//
// Formato: una línea "clave = valor" por ajuste; las que empiezan por # son
// comentarios. Un valor vacío no cambia nada. El fichero se importa solo cuando
// cambia, así no pisa lo que se edite después en el portal. Tras importarlo, las
// contraseñas se borran del fichero (quedan guardadas en la Pi).
package bootcfg

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"teslapi/internal/config"
)

// Secrets son las claves que se borran del fichero tras importarlo.
var Secrets = map[string]bool{"wifi_password": true, "wifi2_password": true, "smb_password": true, "portal_password": true, "setup_ap_password": true}

// Parse devuelve los pares clave → valor (claves en minúsculas, valores sin espacios
// alrededor). Las comillas que envuelven un valor entero se quitan.
func Parse(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

// Result resume una importación.
type Result struct {
	Applied        []string
	Unknown        []string
	PortalPassword string // en claro: quien llama la convierte en hash
}

// Apply vuelca los valores no vacíos sobre c. Un valor no válido es un error y no
// se aplica nada.
func Apply(c *config.Config, v map[string]string) (Result, error) {
	var r Result
	n := *c
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		val := v[k]
		if val == "" || k == "force_wifi" {
			continue
		}
		var err error
		switch k {
		case "language":
			n.Language = strings.ToLower(val)
		case "home_detection":
			switch strings.ToLower(val) {
			case config.TriggerBLE, "bluetooth":
				n.Trigger = config.TriggerBLE
			case config.TriggerWiFi:
				n.Trigger = config.TriggerWiFi
			default:
				err = errors.New("must be bluetooth or wifi")
			}
		case "wifi_ssid":
			n.WiFi.SSID = val
		case "wifi_password":
			n.WiFi.Password = val
		case "wifi2_ssid":
			setWiFi2(&n.WiFi, val, nil)
		case "wifi2_password":
			p := val
			setWiFi2(&n.WiFi, "", &p)
		case "device_address":
			n.Charger.Address = strings.ToUpper(val)
		case "device_name":
			n.Charger.Name = val
		case "extra_devices":
			n.Charger.Extra = parseDevices(val)
		case "device_min_rssi":
			n.Charger.RSSIMin, err = strconv.Atoi(val)
		case "smb_share":
			n.SMB.Share = strings.ReplaceAll(val, `\`, "/")
		case "smb_folder":
			n.SMB.Subdir = val
		case "smb_user":
			n.SMB.User = val
		case "smb_password":
			n.SMB.Password = val
		case "smb_domain":
			n.SMB.Domain = val
		case "smb_version":
			n.SMB.Version = val
		case "smb_encrypt":
			n.SMB.Encrypt, err = parseBool(val)
		case "archive_recent_clips":
			n.Archive.RecentClips, err = parseBool(val)
		case "free_space_percent":
			n.Archive.FreeSpacePct, err = strconv.Atoi(val)
		case "portal_password":
			r.PortalPassword = val
		case "setup_ap_password":
			if len(val) < 8 {
				err = errors.New("must be at least 8 characters (WPA2)")
			}
			n.Setup.APPassword = val
		default:
			r.Unknown = append(r.Unknown, k)
			continue
		}
		if err != nil {
			return Result{}, fmt.Errorf("%s: %w", k, err)
		}
		r.Applied = append(r.Applied, k)
	}
	if err := n.SMB.Validate(); err != nil {
		return Result{}, fmt.Errorf("smb: %w", err)
	}
	if p := r.PortalPassword; p != "" && len(p) < config.MinPortalPassword {
		return Result{}, fmt.Errorf("portal_password: must be at least %d characters", config.MinPortalPassword)
	}
	// Con Wi-Fi, casa, NAS y contraseña del portal ya no hace falta el asistente.
	if n.Complete() && (n.Portal.PasswordHash != "" || r.PortalPassword != "") {
		n.Setup.Done = true
	}
	*c = n
	return r, nil
}

// setWiFi2 fija el nombre y/o la contraseña de la primera red alternativa.
func setWiFi2(w *config.WiFi, ssid string, pass *string) {
	w.Extra = append([]config.WiFiNet(nil), w.Extra...)
	if len(w.Extra) == 0 {
		w.Extra = []config.WiFiNet{{}}
	}
	if ssid != "" {
		w.Extra[0].SSID = ssid
	}
	if pass != nil {
		w.Extra[0].Password = *pass
	}
}

// parseDevices lee una lista separada por comas o punto y coma: cada elemento es una
// dirección BLE (AA:BB:CC:DD:EE:FF) o, si no lo parece, un nombre.
func parseDevices(val string) []config.Device {
	var out []config.Device
	for _, it := range strings.FieldsFunc(val, func(r rune) bool { return r == ',' || r == ';' }) {
		it = strings.TrimSpace(it)
		switch {
		case it == "":
		case macRe.MatchString(it):
			out = append(out, config.Device{Address: strings.ToUpper(it)})
		default:
			out = append(out, config.Device{Name: it})
		}
	}
	return out
}

var macRe = regexp.MustCompile(`^([0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}$`)

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "1", "yes", "true", "on", "si", "sí":
		return true, nil
	case "0", "no", "false", "off":
		return false, nil
	}
	return false, errors.New("must be yes or no")
}

// Fingerprint identifica el contenido que importa: pares no vacíos, sin force_wifi.
func Fingerprint(v map[string]string) string {
	var keys []string
	for k, val := range v {
		if val != "" && k != "force_wifi" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, v[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Scrub devuelve el fichero con los valores secretos vaciados y una nota de cuándo
// se guardaron. Lo demás (comentarios, orden) queda igual.
func Scrub(b []byte, now time.Time) []byte {
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(b))
	nl := "\n"
	if bytes.Contains(b, []byte("\r\n")) {
		nl = "\r\n"
	}
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		if k, v, ok := strings.Cut(t, "="); ok && !strings.HasPrefix(t, "#") {
			k = strings.ToLower(strings.TrimSpace(k))
			if Secrets[k] && strings.TrimSpace(v) != "" {
				out.WriteString("# " + k + ": saved on the Pi " + now.Format("2006-01-02") + ", removed from this file" + nl)
				out.WriteString(k + " =" + nl)
				continue
			}
		}
		out.WriteString(line + nl)
	}
	return out.Bytes()
}

// Rescue dice si el fichero pide la Wi-Fi siempre encendida (force_wifi = 1).
func Rescue(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	on, _ := parseBool(Parse(b)["force_wifi"])
	return on
}

// Importer aplica teslapi.txt a la configuración cuando el fichero cambia.
type Importer struct {
	Path      string // /boot/firmware/teslapi.txt
	StateFile string // huella de lo último importado (en /data/state)
	Store     *config.Store
	// HashPassword convierte portal_password en el hash que se guarda.
	HashPassword func(string) (string, error)
	// WriteBoot reescribe el fichero (la partición de arranque va en solo lectura).
	// nil = no se borran las contraseñas del fichero.
	WriteBoot func(path string, b []byte) error
	Now       func() time.Time
}

// Run devuelve imported=false si no hay fichero o no ha cambiado desde la última vez.
func (im Importer) Run() (r Result, imported bool, err error) {
	raw, err := os.ReadFile(im.Path)
	if errors.Is(err, os.ErrNotExist) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	v := Parse(raw)
	fp := Fingerprint(v)
	if old, _ := os.ReadFile(im.StateFile); strings.TrimSpace(string(old)) == fp {
		return r, false, nil
	}
	var applyErr error
	_, err = im.Store.Update(func(c *config.Config) {
		n := *c
		if r, applyErr = Apply(&n, v); applyErr != nil {
			return
		}
		if r.PortalPassword != "" {
			hash, herr := im.HashPassword(r.PortalPassword)
			if herr != nil {
				applyErr = herr
				return
			}
			n.Portal.PasswordHash = hash
		}
		*c = n
	})
	if applyErr != nil {
		return r, false, applyErr
	}
	if err != nil {
		return r, false, err
	}
	r.PortalPassword = ""
	hasSecret := false
	for k := range Secrets {
		hasSecret = hasSecret || v[k] != ""
	}
	if hasSecret && im.WriteBoot != nil {
		now := time.Now
		if im.Now != nil {
			now = im.Now
		}
		scrubbed := Scrub(raw, now())
		if werr := im.WriteBoot(im.Path, scrubbed); werr == nil {
			fp = Fingerprint(Parse(scrubbed))
		} else {
			err = fmt.Errorf("imported, but the passwords could not be removed from %s: %w", im.Path, werr)
		}
	}
	if werr := os.WriteFile(im.StateFile, []byte(fp+"\n"), 0o600); werr != nil && err == nil {
		err = werr
	}
	return r, true, err
}
