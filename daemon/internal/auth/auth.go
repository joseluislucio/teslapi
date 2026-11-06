// Package auth protege el portal con una contraseña opcional.
//
// La contraseña se guarda como pbkdf2-sha256 con sal. La sesión es una cookie
// firmada con HMAC (caducidad + firma), sin estado en memoria: la Pi se reinicia
// cada vez que el coche se despierta y no debe pedir la contraseña cada vez.
// Cambiar la contraseña cambia la clave y cierra todas las sesiones.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	CookieName = "teslapi_session"
	iterations = 600_000 // lo que recomienda OWASP para PBKDF2-HMAC-SHA256
	SessionTTL = 14 * 24 * time.Hour
	// MaxPassword: tope razonable para no calcular hashes de 64 KB.
	MaxPassword = 1024
)

// Hash devuelve "pbkdf2-sha256$iter$sal$hash" (base64 sin relleno).
func Hash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return hashWith(password, salt, iterations)
}

// HashWithIterations existe para probar la migración de contraseñas antiguas.
func HashWithIterations(password string, iter int) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return hashWith(password, salt, iter)
}

func hashWith(password string, salt []byte, iter int) (string, error) {
	k, err := pbkdf2.Key(sha256.New, password, salt, iter, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iter, enc.EncodeToString(salt), enc.EncodeToString(k)), nil
}

// Check compara en tiempo constante.
func Check(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	got, err := hashWith(password, salt, iter)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(stored)) == 1
}

// NeedsRehash dice si el hash se guardó con menos iteraciones de las actuales.
func NeedsRehash(stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	return err == nil && iter < iterations
}

// Sessions firma y comprueba cookies. La clave vive en un fichero 600 en /data/state.
type Sessions struct {
	KeyFile string

	mu  sync.Mutex
	key []byte
}

func (s *Sessions) load() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil {
		return s.key, nil
	}
	if b, err := os.ReadFile(s.KeyFile); err == nil && len(b) == 32 {
		s.key = b
		return b, nil
	}
	return s.rotateLocked()
}

func (s *Sessions) rotateLocked() ([]byte, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.KeyFile, k, 0o600); err != nil {
		return nil, err
	}
	s.key = k
	return k, nil
}

// Rotate invalida todas las sesiones abiertas.
func (s *Sessions) Rotate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.rotateLocked()
	return err
}

func sign(key []byte, exp int64) string {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(exp))
	m := hmac.New(sha256.New, key)
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(append(b, m.Sum(nil)...))
}

// Issue pone la cookie de sesión. Con HTTPS va marcada Secure.
func (s *Sessions) Issue(w http.ResponseWriter, r *http.Request, now time.Time) error {
	key, err := s.load()
	if err != nil {
		return err
	}
	exp := now.Add(SessionTTL)
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: sign(key, exp.Unix()), Path: "/",
		Expires: exp, MaxAge: int(SessionTTL.Seconds()), HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r != nil && r.TLS != nil})
	return nil
}

func Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// Valid comprueba la cookie de la petición.
func (s *Sessions) Valid(r *http.Request, now time.Time) bool {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != 8+sha256.Size {
		return false
	}
	key, err := s.load()
	if err != nil {
		return false
	}
	exp := int64(binary.BigEndian.Uint64(raw[:8]))
	if now.Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(sign(key, exp)), []byte(c.Value))
}

// Limiter frena los intentos de contraseña por IP, con una espera que se dobla a cada fallo.
// No hace cola: si esa IP debe esperar o ya hay un cálculo en marcha responde enseguida, para
// que nadie pueda dejar sin acceso al dueño llenando una cola. El valor cero está listo.
type Limiter struct {
	// Now sustituye al reloj en las pruebas.
	Now func() time.Time

	busy sync.Mutex // un solo PBKDF2 a la vez: en la Zero 2 W ocupa un núcleo
	mu   sync.Mutex
	ips  map[string]*attempts
}

type attempts struct {
	fails int
	until time.Time
}

const (
	maxWait    = 5 * time.Minute
	maxTracked = 256
)

var (
	ErrWrong   = errors.New("wrong password")
	ErrLimited = errors.New("too many attempts")
)

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// ClientIP es la dirección de quien llama (el portal no va detrás de ningún proxy).
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Try comprueba la contraseña con ok. Devuelve nil, ErrWrong o ErrLimited (sin haberla
// comprobado), y en este último caso cuánto falta para poder reintentar.
func (l *Limiter) Try(ip string, ok func() bool) (time.Duration, error) {
	l.mu.Lock()
	if a := l.ips[ip]; a != nil && l.now().Before(a.until) {
		wait := a.until.Sub(l.now())
		l.mu.Unlock()
		return wait, ErrLimited
	}
	l.mu.Unlock()
	if !l.busy.TryLock() {
		return time.Second, ErrLimited
	}
	good := ok()
	l.busy.Unlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	if good {
		delete(l.ips, ip)
		return 0, nil
	}
	if l.ips == nil {
		l.ips = map[string]*attempts{}
	}
	a := l.ips[ip]
	if a == nil {
		if len(l.ips) >= maxTracked {
			l.forgetOldest()
		}
		a = &attempts{}
		l.ips[ip] = a
	}
	a.fails++
	wait := time.Second << min(a.fails-1, 16)
	if wait > maxWait {
		wait = maxWait
	}
	a.until = l.now().Add(wait)
	return 0, ErrWrong
}

func (l *Limiter) forgetOldest() {
	var oldest string
	var at time.Time
	for ip, a := range l.ips {
		if oldest == "" || a.until.Before(at) {
			oldest, at = ip, a.until
		}
	}
	delete(l.ips, oldest)
}
