package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"teslapi/internal/archive"
	"teslapi/internal/auth"
	"teslapi/internal/config"
	"teslapi/internal/core"
	"teslapi/internal/gadget"
	"teslapi/internal/nas"
	"teslapi/internal/presence"
	"teslapi/internal/snapshot"
	"teslapi/internal/sysinfo"
	"teslapi/internal/wifi"
)

const smbSecret = "SMB-SECRETO-SINTETICO"

type rig struct {
	t     *testing.T
	h     http.Handler
	s     *Server
	d     *core.Daemon
	drive string
}

// newRig monta el portal con controladores falsos. Con password, la instalación está
// terminada y protegida; sin ella, el asistente sigue abierto.
func newRig(t *testing.T, password string) *rig {
	t.Helper()
	dir := t.TempDir()
	state, drive := filepath.Join(dir, "state"), filepath.Join(dir, "drive")
	os.MkdirAll(state, 0o700)
	os.MkdirAll(filepath.Join(drive, "TeslaCam/SavedClips/ev"), 0o755)
	cfg, err := config.Open(filepath.Join(state, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(testConfig(password, t)); err != nil {
		t.Fatal(err)
	}
	ix, err := archive.OpenIndex(filepath.Join(state, "ix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ring := core.NewRing(slog.NewTextHandler(io.Discard, nil))
	d := core.New(core.Deps{
		Cfg: cfg, Index: ix, Log: log, Logs: ring, APSSID: "TeslaPi-TEST",
		Presence: presence.NewMonitor(&presence.Fake{}, func() config.Charger { return cfg.Get().Charger }),
		WiFi:     &wifi.Fake{}, Gadget: &gadget.Fake{}, Snap: &snapshot.Fake{Root: drive},
		NAS: &nas.Local{Path: filepath.Join(dir, "nas")}, Sys: &sysinfo.Reader{Fake: true},
	})
	if err := d.RefreshBrowse(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := &Server{D: d, Log: log, Sessions: &auth.Sessions{KeyFile: filepath.Join(state, "session.key")}}
	return &rig{t: t, h: s.Handler(), s: s, d: d, drive: drive}
}

func testConfig(password string, t *testing.T) config.Config {
	c := config.Defaults()
	c.WiFi = config.WiFi{SSID: "Casa", Password: "clave-casa-1"}
	c.SMB = config.SMB{Share: "//nas/tesla", Subdir: "TeslaCam", User: "u", Password: smbSecret, Version: "3.1.1"}
	if password != "" {
		h, err := auth.Hash(password)
		if err != nil {
			t.Fatal(err)
		}
		c.Portal.PasswordHash, c.Setup.Done = h, true
	}
	return c
}

type reqOpt func(*http.Request)

func cookie(c string) reqOpt { return func(r *http.Request) { r.Header.Set("Cookie", c) } }
func header(k, v string) reqOpt {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

func (g *rig) do(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	g.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1"
	r.RemoteAddr = "192.0.2.10:5000"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	g.h.ServeHTTP(w, r)
	return w
}

func (g *rig) login(password string) string {
	g.t.Helper()
	w := g.do("POST", "/api/auth/login", `{"password":"`+password+`"}`)
	if w.Code != 200 {
		g.t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	return w.Result().Cookies()[0].Name + "=" + w.Result().Cookies()[0].Value
}

// Cada ataque demostrado en la auditoría de seguridad, como prueba de regresión.

func TestForeignHostIsRejected(t *testing.T) {
	g := newRig(t, "")
	foreign := func(r *http.Request) { r.Host = "evil.example" }
	if w := g.do("GET", "/api/config", "", foreign); w.Code != 403 {
		t.Fatalf("API con nombre ajeno (DNS rebinding): %d", w.Code)
	}
	if w := g.do("GET", "/media/pi/x.mp4", "", foreign); w.Code != 403 {
		t.Fatalf("vídeos con nombre ajeno: %d", w.Code)
	}
	for _, ok := range []string{"127.0.0.1:80", "[fe80::1%25wlan0]:80", "[::1]", "localhost:8080"} {
		host := ok
		if w := g.do("GET", "/api/auth", "", func(r *http.Request) { r.Host = host }); w.Code != 200 {
			t.Errorf("Host %s: %d", ok, w.Code)
		}
	}
}

func TestCaptivePortalRedirectsToTheIP(t *testing.T) {
	g := newRig(t, "")
	r := httptest.NewRequest("GET", "/hotspot-detect.html", nil)
	r.Host = "captive.apple.com"
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, addr("10.42.0.1:80")))
	w := httptest.NewRecorder()
	g.h.ServeHTTP(w, r)
	if w.Code != 302 || w.Header().Get("Location") != "http://10.42.0.1:80/" {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Location"))
	}
}

type addr string

func (a addr) Network() string { return "tcp" }
func (a addr) String() string  { return string(a) }

func TestCrossOriginWritesAreRejected(t *testing.T) {
	g := newRig(t, "")
	cases := map[string]reqOpt{
		"Origin ajeno":   header("Origin", "http://evil.example"),
		"Sec-Fetch-Site": header("Sec-Fetch-Site", "cross-site"),
	}
	for name, o := range cases {
		if w := g.do("POST", "/api/refresh", "{}", o); w.Code != 403 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if w := g.do("POST", "/api/setup/finish", `{"password":"atacante1"}`, header("Content-Type", "text/plain")); w.Code != 415 {
		t.Errorf("text/plain: %d", w.Code)
	}
}

func TestSecurityHeadersEverywhere(t *testing.T) {
	g := newRig(t, "")
	for _, p := range []string{"/", "/api/auth", "/media/pi/no-existe.mp4"} {
		h := g.do("GET", p, "").Header()
		if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: %v", p, h)
		}
	}
}

func TestFinishSetupRequiresAPassword(t *testing.T) {
	g := newRig(t, "")
	if w := g.do("POST", "/api/setup/finish", `{}`); w.Code != 400 {
		t.Fatalf("sin contraseña: %d", w.Code)
	}
	if w := g.do("POST", "/api/setup/finish", `{"password":"corta"}`); w.Code != 400 {
		t.Fatalf("corta: %d", w.Code)
	}
	if g.d.Cfg.Get().Setup.Done {
		t.Fatal("no debía terminar")
	}
	w := g.do("POST", "/api/setup/finish", `{"password":"contraseña-buena"}`)
	if w.Code != 200 || len(w.Result().Cookies()) == 0 || !g.d.Cfg.Get().Setup.Done {
		t.Fatalf("con contraseña: %d", w.Code)
	}
}

func TestFinishedSetupCannotBeRunAgain(t *testing.T) {
	g := newRig(t, "clave-duena-1")
	c := g.login("clave-duena-1")
	// La toma de control de la auditoría: cambiar la contraseña sin saber la actual.
	if w := g.do("POST", "/api/setup/finish", `{"password":"del-atacante"}`, cookie(c)); w.Code != 409 {
		t.Fatalf("con sesión: %d", w.Code)
	}
	if !auth.Check("clave-duena-1", g.d.Cfg.Get().Portal.PasswordHash) {
		t.Fatal("la contraseña ha cambiado")
	}
	// Reabrir el asistente tampoco deja cambiarla por aquí.
	if w := g.do("POST", "/api/setup/restart", "", cookie(c)); w.Code != 202 {
		t.Fatalf("restart: %d", w.Code)
	}
	if w := g.do("POST", "/api/setup/finish", `{"password":"del-atacante"}`, cookie(c)); w.Code != 200 {
		t.Fatalf("terminar de nuevo: %d", w.Code)
	}
	if !auth.Check("clave-duena-1", g.d.Cfg.Get().Portal.PasswordHash) {
		t.Fatal("repetir el asistente no debe cambiar la contraseña")
	}
}

func TestPasswordCannotBeRemovedNorShort(t *testing.T) {
	g := newRig(t, "clave-duena-1")
	c := g.login("clave-duena-1")
	for _, body := range []string{`{"current":"clave-duena-1","new":""}`, `{"current":"clave-duena-1","new":"corta"}`} {
		if w := g.do("PUT", "/api/auth/password", body, cookie(c)); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if g.d.Cfg.Get().Portal.PasswordHash == "" {
		t.Fatal("se quitó la contraseña")
	}
}

func TestGuardCannotBeBypassed(t *testing.T) {
	g := newRig(t, "clave-duena-1")
	paths := []string{"/api/config", "/%61pi/config", "/api/auth/../config", "/api/auth/login/../../config", "/api/config/", "/media/pi/x.mp4", "/media/../api/config"}
	for _, p := range paths {
		w := g.do("GET", p, "")
		if w.Code != 401 {
			t.Errorf("%s: %d", p, w.Code)
		}
		if strings.Contains(w.Body.String(), smbSecret) {
			t.Errorf("%s filtra el secreto", p)
		}
	}
	// Lo que redirige o cae en la SPA no devuelve datos.
	for _, p := range []string{"//api/config", "/./api/config", "/API/config", "/assets/../api/config"} {
		if b := g.do("GET", p, "").Body.String(); strings.Contains(b, `"smb"`) {
			t.Errorf("%s devuelve la configuración", p)
		}
	}
}

func TestLogoutRevokesTheSession(t *testing.T) {
	g := newRig(t, "clave-duena-1")
	c := g.login("clave-duena-1")
	if w := g.do("GET", "/api/config", "", cookie(c)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := g.do("POST", "/api/auth/logout", "", cookie(c)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := g.do("GET", "/api/config", "", cookie(c)); w.Code != 401 {
		t.Fatalf("la cookie sigue valiendo tras cerrar sesión: %d", w.Code)
	}
}

func TestLoginBacksOffWithoutBlockingOthers(t *testing.T) {
	g := newRig(t, "clave-duena-1")
	if w := g.do("POST", "/api/auth/login", `{"password":"mala"}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := g.do("POST", "/api/auth/login", `{"password":"mala"}`)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("segundo intento: %d", w.Code)
	}
	// Otra IP entra aunque la primera esté castigada.
	other := func(r *http.Request) { r.RemoteAddr = "192.0.2.20:5000" }
	if w := g.do("POST", "/api/auth/login", `{"password":"clave-duena-1"}`, other); w.Code != 200 {
		t.Fatalf("otra IP: %d", w.Code)
	}
}

func TestLoginStrengthensOldHashes(t *testing.T) {
	g := newRig(t, "clave-duena-1")
	const old210k = "pbkdf2-sha256$210000$"
	h := oldHash(t, "clave-duena-1")
	if !strings.HasPrefix(h, old210k) {
		t.Fatal(h)
	}
	g.d.Cfg.Update(func(c *config.Config) { c.Portal.PasswordHash = h })
	g.login("clave-duena-1")
	if n := g.d.Cfg.Get().Portal.PasswordHash; auth.NeedsRehash(n) || !auth.Check("clave-duena-1", n) {
		t.Fatalf("no se reforzó: %s", n)
	}
}

// oldHash: una contraseña guardada con la versión anterior (PBKDF2 con 210 000 iteraciones).
func oldHash(t *testing.T, p string) string {
	t.Helper()
	h, err := auth.HashWithIterations(p, 210_000)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestConfigRejectsPathTraversalAndOptionInjection(t *testing.T) {
	g := newRig(t, "clave-duena-1")
	c := g.login("clave-duena-1")
	base := g.do("GET", "/api/config", "", cookie(c)).Body.String()
	change := func(mut func(map[string]any)) string {
		var m map[string]any
		json.Unmarshal([]byte(base), &m)
		mut(m["smb"].(map[string]any))
		b, _ := json.Marshal(m)
		return string(b)
	}
	cases := map[string]func(map[string]any){
		"subdir ../state":  func(s map[string]any) { s["subdir"] = "../state" },
		"subdir ../../..":  func(s map[string]any) { s["subdir"] = "../../.." },
		"subdir absoluto":  func(s map[string]any) { s["subdir"] = "/etc" },
		"opciones en vers": func(s map[string]any) { s["version"] = "3.0,sec=none,ip=203.0.113.9" },
		"salto de línea":   func(s map[string]any) { s["user"] = "u\npassword=x" },
		"otro servidor":    func(s map[string]any) { s["share"] = "//atacante/x" },
		"share malformado": func(s map[string]any) { s["share"] = "-o/x" },
	}
	for name, mut := range cases {
		body := change(mut)
		if w := g.do("PUT", "/api/config", body, cookie(c)); w.Code != 400 {
			t.Errorf("PUT %s: %d", name, w.Code)
		}
		if w := g.do("POST", "/api/nas/test", body, cookie(c)); w.Code != 400 {
			t.Errorf("nas/test %s: %d", name, w.Code)
		}
	}
	if g.d.Cfg.Get().SMB.Subdir != "TeslaCam" || g.d.Cfg.Get().SMB.Version != "3.1.1" {
		t.Fatalf("se guardó: %+v", g.d.Cfg.Get().SMB)
	}
	// Lo normal sigue funcionando y no enseña el secreto.
	w := g.do("PUT", "/api/config", change(func(s map[string]any) { s["subdir"] = "Coche/Clips" }), cookie(c))
	if w.Code != 200 || strings.Contains(w.Body.String(), smbSecret) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if g.d.Cfg.Get().SMB.Password != smbSecret {
		t.Fatal("la contraseña guardada debe conservarse")
	}
}

func TestMediaOnlyOpensKnownTypesInline(t *testing.T) {
	g := newRig(t, "")
	dir := filepath.Join(g.drive, "TeslaCam/SavedClips/ev")
	os.WriteFile(filepath.Join(dir, "evil.html"), []byte("<script>alert(1)</script>"), 0o644)
	os.WriteFile(filepath.Join(dir, "noext"), []byte("<html><script>1</script></html>"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.mp4"), []byte("\x00\x00\x00\x18ftypmp42"), 0o644)
	for _, n := range []string{"evil.html", "noext"} {
		w := g.do("GET", "/media/pi/TeslaCam/SavedClips/ev/"+n, "")
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
			t.Errorf("%s: %d %v", n, w.Code, w.Header())
		}
	}
	w := g.do("GET", "/media/pi/TeslaCam/SavedClips/ev/a.mp4", "")
	if w.Header().Get("Content-Type") != "video/mp4" || w.Header().Get("Content-Disposition") != "" {
		t.Errorf("mp4: %v", w.Header())
	}
	w = g.do("GET", "/media/pi/TeslaCam/SavedClips/ev/a.mp4?download", "")
	if !strings.Contains(w.Header().Get("Content-Disposition"), `filename=a.mp4`) {
		t.Errorf("descarga: %v", w.Header())
	}
	for _, p := range []string{"/media/pi/..%2f..%2fetc%2fpasswd", "/media/pi/%2e%2e/state/config.json", "/media/pi/TeslaCam/..%2f..%2f..%2fetc%2fpasswd"} {
		if w := g.do("GET", p, ""); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

func TestDevRouteAbsentInProduction(t *testing.T) {
	g := newRig(t, "")
	if b := g.do("POST", "/api/dev/charger", `{"present":true}`).Body.String(); strings.Contains(b, "present") {
		t.Fatalf("la ruta de desarrollo existe: %s", b)
	}
}

func TestStreamsAreCapped(t *testing.T) {
	g := newRig(t, "")
	g.s.streams.Store(maxStreams)
	if w := g.do("GET", "/api/stream", ""); w.Code != 503 {
		t.Fatalf("%d", w.Code)
	}
}

func TestRedirectHTTPSOnlyAfterSetup(t *testing.T) {
	g := newRig(t, "")
	done := false
	h := g.s.RedirectHTTPS(g.h, func() bool { return done })
	req := func(host string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/auth?a=1", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := req("127.0.0.1:80"); w.Code != 200 {
		t.Fatalf("durante el asistente: %d", w.Code)
	}
	done = true
	if w := req("127.0.0.1:80"); w.Code != 301 || w.Header().Get("Location") != "https://127.0.0.1/api/auth?a=1" {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Location"))
	}
	if w := req("evil.example"); w.Code != 403 {
		t.Fatalf("no debe redirigir a un nombre ajeno: %d %s", w.Code, w.Header().Get("Location"))
	}
}
