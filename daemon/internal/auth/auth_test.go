package auth

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestHashCheck(t *testing.T) {
	h, _ := Hash("clave-larga")
	if !Check("clave-larga", h) || Check("otra", h) || Check("clave-larga", "basura") {
		t.Fatal("hash")
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := &Sessions{KeyFile: filepath.Join(t.TempDir(), "k")}
	now := time.Now()
	w := httptest.NewRecorder()
	s.Issue(w, httptest.NewRequest("GET", "/", nil), now)
	req := func() *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		for _, c := range w.Result().Cookies() {
			r.AddCookie(c)
		}
		return r
	}
	if !s.Valid(req(), now) {
		t.Fatal("cookie recién emitida")
	}
	// Tras un reinicio de la Pi (nueva instancia, misma clave en disco) sigue valiendo.
	if !(&Sessions{KeyFile: s.KeyFile}).Valid(req(), now) {
		t.Fatal("la sesión debe sobrevivir al reinicio")
	}
	if s.Valid(req(), now.Add(SessionTTL+time.Hour)) {
		t.Fatal("caducada")
	}
	if s.Valid(httptest.NewRequest("GET", "/", nil), now) {
		t.Fatal("sin cookie")
	}
	s.Rotate()
	if s.Valid(req(), now) {
		t.Fatal("cambiar la contraseña cierra las sesiones")
	}
}

func TestCookieIsSecureOnlyOverTLS(t *testing.T) {
	s := &Sessions{KeyFile: filepath.Join(t.TempDir(), "k")}
	w := httptest.NewRecorder()
	s.Issue(w, httptest.NewRequest("GET", "/", nil), time.Now())
	if c := w.Result().Cookies()[0]; c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("http: %+v", c)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.TLS = &tls.ConnectionState{}
	w = httptest.NewRecorder()
	s.Issue(w, r, time.Now())
	if !w.Result().Cookies()[0].Secure {
		t.Fatal("con HTTPS la cookie debe ser Secure")
	}
}

func TestLimiterBacksOffPerIPWithoutQueueing(t *testing.T) {
	now := time.Unix(1000, 0)
	l := &Limiter{Now: func() time.Time { return now }}
	calls := 0
	bad := func() bool { calls++; return false }
	if _, err := l.Try("a", bad); !errors.Is(err, ErrWrong) {
		t.Fatal(err)
	}
	// Dentro de la espera ni se comprueba, y responde al momento.
	wait, err := l.Try("a", bad)
	if !errors.Is(err, ErrLimited) || calls != 1 || wait != time.Second {
		t.Fatalf("%v %d %v", err, calls, wait)
	}
	// Otra IP no paga la espera de la primera.
	if _, err := l.Try("b", func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	l.Try("a", bad)
	if wait, _ := l.Try("a", bad); wait != 2*time.Second {
		t.Fatalf("la espera se dobla: %v", wait)
	}
	// Acertar borra los fallos.
	now = now.Add(2 * time.Second)
	if _, err := l.Try("a", func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Try("a", bad); !errors.Is(err, ErrWrong) {
		t.Fatal(err)
	}
	// La espera tiene tope.
	for range 40 {
		now = now.Add(time.Hour)
		l.Try("a", bad)
	}
	if wait, _ := l.Try("a", bad); wait > maxWait {
		t.Fatalf("tope: %v", wait)
	}
}

func TestLimiterRefusesWhileAnotherCheckRuns(t *testing.T) {
	l := &Limiter{}
	done := make(chan struct{})
	started := make(chan struct{})
	go l.Try("a", func() bool { close(started); <-done; return false })
	<-started
	if _, err := l.Try("b", func() bool { t.Fatal("no debía calcular"); return true }); !errors.Is(err, ErrLimited) {
		t.Fatal(err)
	}
	close(done)
}

func TestNeedsRehash(t *testing.T) {
	old, _ := hashWith("x", []byte("0123456789abcdef"), 210_000)
	cur, _ := Hash("x")
	if !NeedsRehash(old) || NeedsRehash(cur) || NeedsRehash("basura") {
		t.Fatal("NeedsRehash")
	}
	if !Check("x", old) {
		t.Fatal("los hashes antiguos deben seguir valiendo")
	}
}
