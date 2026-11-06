// Package api sirve la API REST, el flujo de estado (SSE), los vídeos y el portal.
package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"teslapi/internal/archive"
	"teslapi/internal/auth"
	"teslapi/internal/config"
	"teslapi/internal/core"
	"teslapi/internal/msg"
	"teslapi/internal/presence"
	"teslapi/internal/sdwear"
	"teslapi/internal/shell"
	"teslapi/internal/sysupdate"
)

//go:embed all:dist
var dist embed.FS

// maxStreams limita las conexiones de /api/stream: cada una mantiene una goroutine y una
// conexión abierta en una Pi que está grabando.
const maxStreams = 8

type Server struct {
	D *core.Daemon
	// DevCharger simula ver el cargador. Solo existe en las compilaciones de desarrollo.
	DevCharger func(present bool)
	// AllowedHosts: nombres extra con los que se accede al portal (además de IP y nombre.local).
	AllowedHosts []string
	Log          *slog.Logger
	Updates      *sysupdate.Manager
	SD           *sdwear.Reader
	Reboot       func() error
	Poweroff     func() error
	// Sessions firma las cookies del portal; Limit frena los intentos de contraseña.
	Sessions *auth.Sessions
	Limit    auth.Limiter

	streams atomic.Int32
	hosts   map[string]bool
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/stream", s.stream)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("POST /api/refresh", s.refresh)
	mux.HandleFunc("POST /api/archive", s.archive)
	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("GET /api/logs", s.logs)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.HandleFunc("POST /api/ble/discover", s.discover)
	mux.HandleFunc("GET /api/ble/devices", s.devices)
	mux.HandleFunc("GET /api/system/updates", s.updatesState)
	mux.HandleFunc("POST /api/system/updates/check", s.updatesCheck)
	mux.HandleFunc("POST /api/system/updates/run", s.updatesRun)
	mux.HandleFunc("GET /api/system/sdcard", s.sdcard)
	mux.HandleFunc("POST /api/system/reboot", s.reboot)
	mux.HandleFunc("POST /api/system/poweroff", s.poweroff)
	if s.DevCharger != nil {
		mux.HandleFunc("POST /api/dev/charger", s.devCharger)
	}
	mux.HandleFunc("GET /api/auth", s.authState)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("PUT /api/auth/password", s.setPassword)
	mux.HandleFunc("GET /api/setup", s.setupState)
	mux.HandleFunc("POST /api/setup/wifi", s.setupWiFi)
	mux.HandleFunc("POST /api/setup/finish", s.setupFinish)
	mux.HandleFunc("POST /api/setup/restart", s.setupRestart)
	mux.HandleFunc("POST /api/nas/test", s.nasTest)
	mux.HandleFunc("GET /media/{source}/{rel...}", s.media)
	mux.Handle("/", s.spa())
	s.hosts = map[string]bool{"localhost": true}
	if h, err := os.Hostname(); err == nil {
		h = strings.ToLower(strings.TrimSuffix(h, ".local"))
		s.hosts[h], s.hosts[h+".local"] = true, true
	}
	for _, h := range s.AllowedHosts {
		s.hosts[strings.ToLower(h)] = true
	}
	// De fuera adentro: cabeceras, nombre con el que se llega, origen de la petición, sesión.
	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, 403, msg.Err("error.cross_origin"))
	}))
	return securityHeaders(s.hostCheck(csrf.Handler(s.guard(mux))))
}

// csp: todo sale de la propia Pi menos las teselas del mapa, que el usuario activa a mano.
// Los estilos en línea los necesitan Svelte y Leaflet.
const csp = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: https://tile.openstreetmap.org; media-src 'self'; connect-src 'self'; font-src 'self'; " +
	"manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		// La política de teselas de OpenStreetMap pide que se envíe el origen.
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// hostAllowed: una IP o un nombre propio. Rechazar el resto frena el DNS rebinding, en el
// que una web ajena apunta su propio nombre a la IP de la Pi para saltarse el mismo origen.
func (s *Server) hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if ip, _, _ := strings.Cut(host, "%"); net.ParseIP(ip) != nil {
		return true
	}
	return s.hosts[host]
}

// hostCheck rechaza la API con un nombre ajeno. En el resto de rutas redirige a la IP de la
// Pi: en la red del asistente todo nombre resuelve a ella, y así el móvil abre el portal.
func (s *Server) hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.hostAllowed(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/media/") {
			writeErr(w, 403, msg.Err("error.host_not_allowed"))
			return
		}
		addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		http.Redirect(w, r, scheme+"://"+addr.String()+"/", http.StatusFound)
	})
}

// RedirectHTTPS manda a HTTPS con el asistente ya terminado. Antes no: el móvil entra por la
// red del asistente y un certificado propio solo estorbaría. Un nombre no admitido sigue su
// camino normal y lo rechaza hostCheck.
func (s *Server) RedirectHTTPS(next http.Handler, done func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !done() || !s.hostAllowed(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if strings.Contains(host, ":") {
			host = "[" + strings.Trim(host, "[]") + "]"
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}

// guard exige sesión en la API y los vídeos cuando hay contraseña. Los ficheros del
// portal y la comprobación de sesión quedan abiertos (la pantalla de acceso los necesita).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		open := !(strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/media/")) ||
			r.URL.Path == "/api/auth" || r.URL.Path == "/api/auth/login"
		if !open && s.D.Cfg.Get().Portal.PasswordHash != "" && !s.Sessions.Valid(r, time.Now()) {
			writeErr(w, 401, msg.Err("auth.required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// writeErr responde {"error": {code, args, detail}}; el portal lo traduce.
func writeErr(w http.ResponseWriter, code int, err error) {
	var me *msg.Error
	if errors.As(err, &me) {
		writeJSON(w, code, map[string]any{"error": me.Msg})
		return
	}
	writeJSON(w, code, map[string]any{"error": msg.Msg{Code: "error.generic", Detail: err.Error()}})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	// Solo JSON: text/plain lo puede enviar cualquier web sin pedir permiso al navegador.
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeErr(w, 415, msg.Err("error.bad_content_type"))
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v); err != nil {
		writeErr(w, 400, msg.Wrap("error.bad_request", err))
		return false
	}
	return true
}

func (s *Server) authState(w http.ResponseWriter, r *http.Request) {
	c := s.D.Cfg.Get()
	required := c.Portal.PasswordHash != ""
	writeJSON(w, 200, map[string]any{"required": required, "authenticated": !required || s.Sessions.Valid(r, time.Now()),
		"setup_done": c.Setup.Done, "language": c.Language})
}

// checkPassword pasa la contraseña por el limitador y responde si falla. Devuelve true si es buena.
func (s *Server) checkPassword(w http.ResponseWriter, r *http.Request, password, hash string, wrong int) bool {
	ip := auth.ClientIP(r)
	if len(password) > auth.MaxPassword {
		writeErr(w, wrong, msg.Err("auth.wrong_password"))
		return false
	}
	wait, err := s.Limit.Try(ip, func() bool { return auth.Check(password, hash) })
	switch {
	case err == nil:
		return true
	case errors.Is(err, auth.ErrLimited):
		sec := int(wait.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(sec))
		writeErr(w, 429, msg.Err("auth.too_many", "seconds", sec))
	default:
		s.Log.Warn("wrong portal password", "ip", ip, "path", r.URL.Path)
		writeErr(w, wrong, msg.Err("auth.wrong_password"))
	}
	return false
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var b struct{ Password string }
	if !decode(w, r, &b) {
		return
	}
	hash := s.D.Cfg.Get().Portal.PasswordHash
	if hash == "" {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if !s.checkPassword(w, r, b.Password, hash, 401) {
		return
	}
	// Las contraseñas guardadas con menos iteraciones se refuerzan al entrar.
	if auth.NeedsRehash(hash) {
		if nh, err := auth.Hash(b.Password); err == nil {
			if _, err := s.D.Cfg.Update(func(c *config.Config) { c.Portal.PasswordHash = nh }); err != nil {
				s.Log.Warn("rehash portal password", "err", err)
			}
		}
	}
	if err := s.Sessions.Issue(w, r, time.Now()); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// logout cierra todas las sesiones: la cookie no guarda estado, así que la única forma de
// que una robada deje de valer es cambiar la clave con la que se firman.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.Sessions.Rotate(); err != nil {
		writeErr(w, 500, err)
		return
	}
	auth.Clear(w)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func validNewPassword(p string) error {
	switch {
	case p == "":
		return msg.Err("auth.password_required")
	case len(p) < config.MinPortalPassword:
		return msg.Err("auth.password_short", "min", config.MinPortalPassword)
	case len(p) > auth.MaxPassword:
		return msg.Err("auth.password_long", "max", auth.MaxPassword)
	}
	return nil
}

// setPassword cambia la contraseña del portal y cierra las demás sesiones. No se puede quitar.
func (s *Server) setPassword(w http.ResponseWriter, r *http.Request) {
	var b struct{ Current, New string }
	if !decode(w, r, &b) {
		return
	}
	hash := s.D.Cfg.Get().Portal.PasswordHash
	if hash != "" && !s.checkPassword(w, r, b.Current, hash, 403) {
		return
	}
	if err := validNewPassword(b.New); err != nil {
		writeErr(w, 400, err)
		return
	}
	nh, err := auth.Hash(b.New)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if _, err := s.D.Cfg.Update(func(c *config.Config) { c.Portal.PasswordHash = nh }); err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := s.Sessions.Rotate(); err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := s.Sessions.Issue(w, r, time.Now()); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) setupState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.D.Setup(r.Context()))
}

func (s *Server) setupWiFi(w http.ResponseWriter, r *http.Request) {
	var b struct{ SSID, Password, Language string }
	if !decode(w, r, &b) {
		return
	}
	if err := s.D.SetupWiFi(b.SSID, b.Password, b.Language); err != nil {
		writeErr(w, 409, err)
		return
	}
	writeJSON(w, 202, map[string]bool{"ok": true})
}

// setupFinish termina el asistente, y solo una vez: con la instalación hecha, cambiar la
// contraseña exige la actual. Si no hay contraseña todavía, es obligatorio ponerla aquí.
func (s *Server) setupFinish(w http.ResponseWriter, r *http.Request) {
	var b struct{ Language, Password string }
	if !decode(w, r, &b) {
		return
	}
	c := s.D.Cfg.Get()
	if c.Setup.Done {
		writeErr(w, 409, msg.Err("setup.already_done"))
		return
	}
	hash := ""
	if c.Portal.PasswordHash == "" {
		if err := validNewPassword(b.Password); err != nil {
			writeErr(w, 400, err)
			return
		}
		var err error
		if hash, err = auth.Hash(b.Password); err != nil {
			writeErr(w, 500, err)
			return
		}
	}
	if err := s.D.FinishSetup(b.Language, hash); err != nil {
		writeErr(w, 500, err)
		return
	}
	if hash != "" {
		if err := s.Sessions.Rotate(); err != nil {
			writeErr(w, 500, err)
			return
		}
		if err := s.Sessions.Issue(w, r, time.Now()); err != nil {
			writeErr(w, 500, err)
			return
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) setupRestart(w http.ResponseWriter, r *http.Request) {
	if err := s.D.RestartSetup(); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 202, map[string]bool{"ok": true})
}

// nasTest comprueba el destino SMB tal como está en el formulario, sin guardarlo.
func (s *Server) nasTest(w http.ResponseWriter, r *http.Request) {
	var c config.Config
	if !decode(w, r, &c) {
		return
	}
	merged, err := c.MergeSecrets(s.D.Cfg.Get())
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	smb := merged.SMB
	if smb.Subdir == "" {
		smb.Subdir = "TeslaCam"
	}
	if err := smb.Validate(); err != nil {
		writeErr(w, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if err := s.D.NAS.Test(ctx, smb); err != nil {
		writeErr(w, 422, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.D.Status(r.Context()))
}

// stream envía el estado cada 2 s por Server-Sent Events.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, msg.Err("error.no_streaming"))
		return
	}
	if s.streams.Add(1) > maxStreams {
		s.streams.Add(-1)
		w.Header().Set("Retry-After", "10")
		writeErr(w, 503, msg.Err("error.too_many_streams"))
		return
	}
	defer s.streams.Add(-1)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		b, _ := json.Marshal(s.D.Status(r.Context()))
		if _, err := w.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
			return
		}
		fl.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("source")
	if src == "" {
		src = "pi"
	}
	evs, err := s.D.Events(src)
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	if evs == nil {
		evs = []core.EventView{}
	}
	writeJSON(w, 200, evs)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.D.RefreshBrowse(ctx); err != nil {
		writeErr(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) archive(w http.ResponseWriter, r *http.Request) {
	if err := s.D.RequestArchive(); err != nil {
		writeErr(w, 409, err)
		return
	}
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	h := s.D.Index.History(100)
	if h == nil {
		h = []archive.Run{}
	}
	writeJSON(w, 200, h)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.D.Logs.Entries())
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.D.Cfg.Get().Redacted())
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var c config.Config
	if !decode(w, r, &c) {
		return
	}
	c, err := c.MergeSecrets(s.D.Cfg.Get())
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := validate(c); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.D.Cfg.Set(c); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.Log.Info("configuración guardada")
	writeJSON(w, 200, c.Redacted())
}

func validate(c config.Config) error {
	switch {
	case c.Trigger != config.TriggerBLE && c.Trigger != config.TriggerWiFi:
		return msg.Err("config.invalid_trigger")
	case c.Charger.RSSIMin < -120 || c.Charger.RSSIMin > 0:
		return msg.Err("config.rssi_range")
	case c.Charger.ScanOnSec < 1 || c.Charger.ScanPeriodSec < c.Charger.ScanOnSec:
		return msg.Err("config.scan_cycle")
	case c.Charger.AbsentAfterSec < 30:
		return msg.Err("config.absent_min", "min", 30)
	case c.SMB.Validate() != nil:
		return c.SMB.Validate()
	case c.Archive.FreeSpacePct < 0 || c.Archive.FreeSpacePct > 80:
		return msg.Err("config.free_range")
	case c.Archive.RepeatMin < 5:
		return msg.Err("config.repeat_min", "min", 5)
	case c.SDCard.Profile != "custom" && SDProfileUnknown(c.SDCard.Profile):
		return msg.Err("config.sd_profile")
	case c.SDCard.Profile == "custom" && (c.SDCard.PECycles < 100 || c.SDCard.PECycles > 200000 || c.SDCard.WAF < 1 || c.SDCard.WAF > 10):
		return msg.Err("config.sd_custom")
	case c.WiFi.Password != "" && len(c.WiFi.Password) < 8:
		return msg.Err("setup.wifi_password_short")
	}
	for _, n := range c.WiFi.Extra {
		if n.Password != "" && len(n.Password) < 8 {
			return msg.Err("setup.wifi_password_short")
		}
	}
	return nil
}

func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	s.D.Presence.Discover(60 * time.Second)
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	active, list := s.D.Presence.Discovered()
	if list == nil {
		list = []presence.Sighting{}
	}
	writeJSON(w, 200, map[string]any{"active": active, "devices": list})
}

func (s *Server) reboot(w http.ResponseWriter, r *http.Request) {
	if s.Reboot == nil {
		writeErr(w, 501, msg.Err("error.dev_only"))
		return
	}
	writeJSON(w, 202, map[string]bool{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := s.Reboot(); err != nil {
			s.Log.Error("reinicio", "err", err)
		}
	}()
}

func (s *Server) poweroff(w http.ResponseWriter, r *http.Request) {
	if s.Poweroff == nil {
		writeErr(w, 501, msg.Err("error.dev_only"))
		return
	}
	writeJSON(w, 202, map[string]bool{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := s.Poweroff(); err != nil {
			s.Log.Error("apagado", "err", err)
		}
	}()
}

func (s *Server) devCharger(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Present bool `json:"present"`
	}
	if !decode(w, r, &body) {
		return
	}
	s.DevCharger(body.Present)
	writeJSON(w, 200, map[string]bool{"present": body.Present})
}

// inlineTypes: lo único que el portal abre en el navegador. Lo que haya en el pendrive o en
// el NAS lo puede haber puesto cualquiera, y un .html abierto aquí tendría la sesión del dueño.
var inlineTypes = map[string]string{".mp4": "video/mp4", ".png": "image/png", ".json": "application/json"}

// media sirve un fichero del snapshot ("pi") o del NAS ("nas") con soporte de Range.
// os.Root impide salir de la carpeta con "..", enlaces simbólicos, etc.
func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	src, rel := r.PathValue("source"), r.PathValue("rel")
	root, ok := s.D.MediaRoot(src)
	if !ok {
		writeErr(w, 404, msg.Err("events.no_source"))
		return
	}
	if src == "nas" {
		rel = strings.TrimPrefix(rel, "TeslaCam/")
	}
	rt, err := os.OpenRoot(root)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	defer rt.Close()
	f, err := rt.Open(path.Clean(rel))
	if err != nil {
		writeErr(w, 404, msg.Err("error.not_found"))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		writeErr(w, 404, msg.Err("error.not_found"))
		return
	}
	ct, inline := inlineTypes[strings.ToLower(path.Ext(rel))]
	if !inline {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	if !inline || r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(rel)}))
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, path.Base(rel), st.ModTime(), f)
}

// spa sirve el portal compilado. Usa la versión .br/.gz precomprimida si el
// navegador la acepta (la Zero 2 W no comprime al vuelo) y cae a index.html
// para las rutas del cliente.
func (s *Server) spa() http.Handler {
	sub, _ := fs.Sub(dist, "dist")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			p = "index.html"
		}
		if p == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		ae := r.Header.Get("Accept-Encoding")
		for _, enc := range []struct{ ext, name string }{{".br", "br"}, {".gz", "gzip"}} {
			if !strings.Contains(ae, enc.name) {
				continue
			}
			if b, err := fs.ReadFile(sub, p+enc.ext); err == nil {
				w.Header().Set("Content-Encoding", enc.name)
				w.Header().Set("Vary", "Accept-Encoding")
				w.Header().Set("Content-Type", contentType(p))
				w.Write(b)
				return
			}
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType(p))
		w.Write(b)
	})
}

func contentType(p string) string {
	switch path.Ext(p) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".woff2":
		return "font/woff2"
	case ".json", ".webmanifest":
		return "application/json"
	}
	return "application/octet-stream"
}

// SystemPoweroff apaga la Pi; systemd para los servicios y sincroniza los discos antes.
func SystemPoweroff() error {
	_, err := shell.Run(context.Background(), "systemctl", "poweroff")
	return err
}

// SystemReboot reinicia la Pi.
func SystemReboot() error {
	_, err := shell.Run(context.Background(), "systemctl", "reboot")
	return err
}

// SDProfileUnknown dice si el perfil de tarjeta no existe (un fichero antiguo sin perfil vale: es consumer).
func SDProfileUnknown(p string) bool {
	if p == "" {
		return false
	}
	_, ok := config.SDProfiles[p]
	return !ok
}

func (s *Server) updatesState(w http.ResponseWriter, r *http.Request) {
	if s.Updates == nil {
		writeErr(w, 501, msg.Err("error.dev_only"))
		return
	}
	writeJSON(w, 200, s.Updates.Snapshot())
}

// updateResult traduce lo que devuelve el gestor: motivo de rechazo (409), ya en marcha (409) o aceptado (202).
func updateResult(w http.ResponseWriter, e *msg.Msg, err error) {
	switch {
	case err != nil:
		writeErr(w, 409, msg.Err("update.in_progress"))
	case e != nil:
		writeJSON(w, 409, map[string]any{"error": e})
	default:
		writeJSON(w, 202, map[string]bool{"ok": true})
	}
}

func (s *Server) updatesCheck(w http.ResponseWriter, r *http.Request) {
	if s.Updates == nil {
		writeErr(w, 501, msg.Err("error.dev_only"))
		return
	}
	e, err := s.Updates.Check()
	updateResult(w, e, err)
}

func (s *Server) updatesRun(w http.ResponseWriter, r *http.Request) {
	if s.Updates == nil {
		writeErr(w, 501, msg.Err("error.dev_only"))
		return
	}
	var in struct {
		Group string `json:"group"`
	}
	if !decode(w, r, &in) {
		return
	}
	e, err := s.Updates.Run(sysupdate.Group(in.Group))
	updateResult(w, e, err)
}

func (s *Server) sdcard(w http.ResponseWriter, r *http.Request) {
	if s.SD == nil {
		writeErr(w, 501, msg.Err("error.dev_only"))
		return
	}
	writeJSON(w, 200, s.SD.Read())
}
