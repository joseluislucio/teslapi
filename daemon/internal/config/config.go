// Package config carga y guarda la configuración persistente de teslapid.
// Vive en /data/state/config.json (la raíz es de solo lectura) con permisos 600.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"teslapi/internal/msg"
)

// MinPortalPassword es la longitud mínima de la contraseña del portal.
const MinPortalPassword = 8

type Charger struct {
	// Address es la MAC BLE del dispositivo de casa (cargador, baliza…). Si va vacía se usa Name.
	Address string `json:"address"`
	// Name casa por subcadena con el nombre BLE anunciado (útil si la MAC es aleatoria).
	Name string `json:"name"`
	// RSSIMin descarta lecturas más débiles (dBm). Evita "verlo" desde la calle.
	RSSIMin int `json:"rssi_min"`
	// SeenWindows: ventanas con avistamiento, de las últimas 3, para declararlo presente.
	SeenWindows int `json:"seen_windows"`
	// AbsentAfterSec: segundos sin verlo para declararlo ausente.
	AbsentAfterSec int `json:"absent_after_sec"`
	// ScanOnSec / ScanPeriodSec: ciclo de trabajo del escaneo BLE.
	ScanOnSec     int `json:"scan_on_sec"`
	ScanPeriodSec int `json:"scan_period_sec"`
	// Extra: otros dispositivos de casa (baliza, altavoz…). Ver cualquiera de ellos, además
	// del principal, cuenta como estar en casa (p. ej. para trabajar con la Pi en el salón).
	Extra []Device `json:"extra,omitempty"`
}

// Device identifica un dispositivo BLE por dirección o, si esta cambia, por nombre.
type Device struct {
	Address string `json:"address"`
	Name    string `json:"name"`
}

// Devices devuelve el dispositivo principal y los extra, sin los vacíos.
func (c Charger) Devices() []Device {
	var out []Device
	if c.Address != "" || c.Name != "" {
		out = append(out, Device{Address: c.Address, Name: c.Name})
	}
	for _, d := range c.Extra {
		if d.Address != "" || d.Name != "" {
			out = append(out, d)
		}
	}
	return out
}

// SDCard son los supuestos con los que se estima la vida de la microSD (ver package sdwear).
type SDCard struct {
	// Profile: consumer | endurance | industrial | custom. Con custom valen PECycles y WAF.
	Profile  string  `json:"profile"`
	PECycles int     `json:"pe_cycles"`
	WAF      float64 `json:"waf"`
}

// SDProfiles son los perfiles de serie: ciclos de borrado y amplificación de escritura típicos.
var SDProfiles = map[string]SDCard{
	"consumer":   {Profile: "consumer", PECycles: 1000, WAF: 2},
	"endurance":  {Profile: "endurance", PECycles: 3000, WAF: 1.5},
	"industrial": {Profile: "industrial", PECycles: 10000, WAF: 1.3},
}

// Resolved devuelve los valores que se aplican (los del perfil, o los propios si es custom).
func (s SDCard) Resolved() SDCard {
	if p, ok := SDProfiles[s.Profile]; ok {
		return p
	}
	if s.Profile == "custom" && s.PECycles > 0 && s.WAF >= 1 {
		return s
	}
	return SDProfiles["consumer"]
}

type WiFi struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
	// Extra: redes alternativas (otra casa, la del banco de pruebas…). Se prueban por
	// orden tras la principal, y estar conectada a cualquiera cuenta como Wi-Fi de casa.
	Extra []WiFiNet `json:"extra,omitempty"`
}

type WiFiNet struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
}

// Networks devuelve la red principal y las alternativas, sin las que no tienen nombre.
func (w WiFi) Networks() []WiFiNet {
	var out []WiFiNet
	if w.SSID != "" {
		out = append(out, WiFiNet{SSID: w.SSID, Password: w.Password})
	}
	for _, n := range w.Extra {
		if n.SSID != "" {
			out = append(out, n)
		}
	}
	return out
}

// Has dice si ssid es una de las redes configuradas.
func (w WiFi) Has(ssid string) bool {
	for _, n := range w.Networks() {
		if n.SSID == ssid {
			return true
		}
	}
	return false
}

type SMB struct {
	// Share en formato //host/recurso.
	Share    string `json:"share"`
	Subdir   string `json:"subdir"`
	User     string `json:"user"`
	Password string `json:"password"`
	Domain   string `json:"domain"`
	Version  string `json:"version"`
	// Encrypt pide cifrado SMB3 (opción seal de mount.cifs). Lo tiene que admitir el
	// servidor y cuesta CPU en la Pi: por eso es opcional.
	Encrypt bool `json:"encrypt,omitempty"`
}

var shareRe = regexp.MustCompile(`^//[A-Za-z0-9._\[\]:][A-Za-z0-9._\[\]:-]*/[^/]`)

// smbVersions son los valores admitidos de vers=. El valor llega a la opción -o de mount:
// sin lista cerrada, "3.0,sec=none,ip=…" cambia cómo y contra quién se monta.
var smbVersions = map[string]bool{"": true, "3.1.1": true, "3.11": true, "3.02": true, "3.0": true, "2.1": true}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// Validate rechaza lo que no debe llegar a mount ni al fichero de credenciales. Subdir debe
// quedarse dentro del recurso: con "../.." el portal serviría y escribiría fuera del NAS.
func (s SMB) Validate() error {
	share := strings.ReplaceAll(s.Share, `\`, "/")
	switch {
	case s.Share != "" && !shareRe.MatchString(share):
		return msg.Err("config.smb_share_format")
	case s.Subdir != "" && (!filepath.IsLocal(s.Subdir) || strings.Contains(s.Subdir, `\`)):
		return msg.Err("config.smb_folder_invalid")
	case !smbVersions[s.Version]:
		return msg.Err("config.smb_version_invalid")
	case hasControl(s.Share) || hasControl(s.Subdir) || hasControl(s.User) || hasControl(s.Password) || hasControl(s.Domain):
		return msg.Err("config.smb_field_invalid")
	}
	return nil
}

// host devuelve el servidor de //host/recurso, en minúsculas.
func (s SMB) host() string {
	h, _, _ := strings.Cut(strings.TrimPrefix(strings.ReplaceAll(s.Share, `\`, "/"), "//"), "/")
	return strings.ToLower(h)
}

type Archive struct {
	RecentClips bool `json:"recent_clips"`
	// MinClipBytes: los .mp4 más pequeños son restos de grabaciones cortadas.
	MinClipBytes int64 `json:"min_clip_bytes"`
	// SettleSec: no se copian ficheros cuyo mtime esté a menos de esto del más reciente
	// (el coche podría estar escribiéndolos cuando se hizo el snapshot).
	SettleSec int `json:"settle_sec"`
	// FreeSpacePct: por debajo de este % libre en el pendrive se borran clips ya archivados.
	FreeSpacePct int `json:"free_space_pct"`
	// RepeatMin: mientras el cargador siga presente, se repite el archivado cada N minutos.
	RepeatMin int `json:"repeat_min"`
}

// Trigger: cómo sabe la Pi que el coche está en casa.
const (
	// TriggerBLE: ve un dispositivo Bluetooth fijo (cargador, baliza). La Wi-Fi
	// solo se enciende entonces: es lo que menos consume.
	TriggerBLE = "ble"
	// TriggerWiFi: la Wi-Fi queda en espera y casa cuando se conecta a la red de
	// casa. No necesita nada más, pero la radio va encendida siempre.
	TriggerWiFi = "wifi"
)

type Portal struct {
	// PasswordHash: pbkdf2-sha256 (ver internal/auth). Vacío = portal sin contraseña.
	PasswordHash string `json:"password_hash,omitempty"`
	// PasswordSet solo se rellena en la versión que va a la API.
	PasswordSet bool `json:"password_set,omitempty"`
}

type Setup struct {
	// Done: el asistente terminó. Mientras no, la Pi abre su propia red si no tiene Wi-Fi.
	Done bool `json:"done"`
	// APPassword: contraseña de la red del asistente. Vacía = red abierta.
	APPassword string `json:"ap_password,omitempty"`
}

type Config struct {
	// Language: idioma por defecto del portal (es, en, fr, de, it, pt). Vacío = el del navegador.
	Language string  `json:"language"`
	Trigger  string  `json:"trigger"`
	Charger  Charger `json:"charger"`
	WiFi     WiFi    `json:"wifi"`
	SMB      SMB     `json:"smb"`
	Archive  Archive `json:"archive"`
	Portal   Portal  `json:"portal"`
	Setup    Setup   `json:"setup"`
	SDCard   SDCard  `json:"sdcard"`
}

// HomeConfigured dice si hay forma de saber que el coche está en casa.
func (c Config) HomeConfigured() bool {
	if c.Trigger == TriggerWiFi {
		return c.WiFi.SSID != ""
	}
	return len(c.Charger.Devices()) > 0
}

// Complete: hay todo lo necesario para funcionar solo (Wi-Fi, casa y NAS).
func (c Config) Complete() bool {
	return c.WiFi.SSID != "" && c.HomeConfigured() && c.SMB.Share != ""
}

func Defaults() Config {
	return Config{
		Trigger: TriggerBLE,
		Charger: Charger{RSSIMin: -90, SeenWindows: 2, AbsentAfterSec: 180, ScanOnSec: 2, ScanPeriodSec: 10},
		SDCard:  SDProfiles["consumer"],
		SMB:     SMB{Subdir: "TeslaCam", Version: "3.1.1"},
		Archive: Archive{RecentClips: true, MinClipBytes: 100_000, SettleSec: 90, FreeSpacePct: 20, RepeatMin: 30},
	}
}

// Store guarda la configuración y la serializa en disco de forma atómica.
type Store struct {
	path string
	mu   sync.RWMutex
	cfg  Config
	upd  sync.Mutex // serializa Update (leer, cambiar, guardar)
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, cfg: Defaults()}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.cfg); err != nil {
		return nil, err
	}
	// Configuraciones anteriores al asistente: si ya estaba todo, no se vuelve a pedir.
	var keys map[string]json.RawMessage
	if json.Unmarshal(b, &keys) == nil {
		if _, ok := keys["setup"]; !ok {
			s.cfg.Setup.Done = s.cfg.Complete()
		}
	}
	// Sin contraseña no hay instalación terminada: el asistente la pide. Cubre lo aprovisionado
	// por script y las versiones en las que era opcional.
	if s.cfg.Portal.PasswordHash == "" {
		s.cfg.Setup.Done = false
	}
	if s.cfg.Trigger == "" {
		s.cfg.Trigger = TriggerBLE
	}
	return s, nil
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Store) Set(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.cfg = c
	return nil
}

const secretMask = "••••••••" // #nosec G101 -- es la máscara que ve el portal, no una clave

// Update aplica fn sobre la configuración actual y la guarda.
func (s *Store) Update(fn func(*Config)) (Config, error) {
	s.upd.Lock()
	defer s.upd.Unlock()
	s.mu.RLock()
	c := s.cfg
	s.mu.RUnlock()
	fn(&c)
	return c, s.Set(c)
}

// Redacted devuelve la configuración sin contraseñas, para la API.
func (c Config) Redacted() Config {
	c.Portal = Portal{PasswordSet: c.Portal.PasswordHash != ""}
	c.Setup.APPassword = ""
	if c.WiFi.Password != "" {
		c.WiFi.Password = secretMask
	}
	// Copia de las alternativas: no se debe tocar la configuración viva.
	c.WiFi.Extra = append([]WiFiNet(nil), c.WiFi.Extra...)
	for i := range c.WiFi.Extra {
		if c.WiFi.Extra[i].Password != "" {
			c.WiFi.Extra[i].Password = secretMask
		}
	}
	c.Charger.Extra = append([]Device(nil), c.Charger.Extra...)
	if c.SMB.Password != "" {
		c.SMB.Password = secretMask
	}
	return c
}

// MergeSecrets conserva las contraseñas actuales si el cliente devuelve la máscara, pero
// solo para el mismo destino: si no, cualquiera con acceso al portal podría mandar la
// contraseña guardada a un servidor suyo o probar la clave Wi-Fi contra otra red.
// La del portal y el estado del asistente no se cambian por aquí (tienen su API).
func (c Config) MergeSecrets(old Config) (Config, error) {
	c.Portal = old.Portal
	c.Setup = old.Setup
	c.WiFi.Extra = append([]WiFiNet(nil), c.WiFi.Extra...)
	stored := func(ssid string) (string, bool) {
		for _, o := range old.WiFi.Networks() {
			if o.SSID == ssid {
				return o.Password, true
			}
		}
		return "", false
	}
	if c.WiFi.Password == secretMask {
		p, ok := stored(c.WiFi.SSID)
		if !ok {
			return c, msg.Err("config.wifi_secret_rebind", "ssid", c.WiFi.SSID)
		}
		c.WiFi.Password = p
	}
	for i, n := range c.WiFi.Extra {
		if n.Password == secretMask {
			p, ok := stored(n.SSID)
			if !ok {
				return c, msg.Err("config.wifi_secret_rebind", "ssid", n.SSID)
			}
			c.WiFi.Extra[i].Password = p
		}
	}
	if c.SMB.Password == secretMask {
		if c.SMB.host() != old.SMB.host() || c.SMB.User != old.SMB.User || c.SMB.Domain != old.SMB.Domain {
			return c, msg.Err("config.smb_secret_rebind")
		}
		c.SMB.Password = old.SMB.Password
	}
	return c, nil
}
