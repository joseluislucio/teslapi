// Package nas monta el recurso SMB de destino del archivado.
package nas

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"teslapi/internal/config"
	"teslapi/internal/msg"
)

type Mounter interface {
	// Mount monta el recurso y devuelve la carpeta de destino (recurso/Subdir).
	Mount(ctx context.Context, c config.SMB) (string, error)
	Unmount(ctx context.Context) error
	// Dir devuelve la carpeta de destino si está montado.
	Dir() (string, bool)
	// Test comprueba que se puede montar y escribir, sin tocar el montaje en uso.
	Test(ctx context.Context, c config.SMB) error
}

// Host extrae el servidor de //host/recurso.
func Host(share string) string {
	s := strings.TrimPrefix(strings.ReplaceAll(share, `\`, "/"), "//")
	h, _, _ := strings.Cut(s, "/")
	return h
}

// Port es el puerto SMB. Solo se cambia en las pruebas de desarrollo (-dev-smb-port).
var Port = "445"

// Reachable comprueba que el puerto 445 responde, para dar un error claro
// ("el servidor no responde") en vez del genérico de mount.cifs.
func Reachable(ctx context.Context, share string, timeout time.Duration) error {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(Host(share), Port))
	if err != nil {
		return msg.Wrap("nas.unreachable", err, "host", Host(share))
	}
	c.Close()
	return nil
}

// Local es el destino del modo desarrollo: una carpeta normal.
type Local struct {
	Path string

	mu      sync.Mutex
	dir     string
	mounted bool
}

// Test en desarrollo comprueba de verdad que el servidor responde (puerto 445) y
// que se puede escribir en la carpeta local que hace de NAS.
func (l *Local) Test(ctx context.Context, c config.SMB) error {
	if c.Share == "" {
		return msg.Err("nas.not_configured")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if err := Reachable(ctx, c.Share, 3*time.Second); err != nil {
		return err
	}
	if err := os.MkdirAll(l.Path, 0o755); err != nil {
		return err
	}
	d, err := subdirIn(l.Path, c.Subdir)
	if err != nil {
		return msg.Wrap("nas.not_writable", err, "folder", c.Subdir)
	}
	return probe(d, c.Subdir)
}

// subdirIn crea la carpeta de destino sin salir de root, ni con ".." ni por un enlace
// simbólico que haya puesto el servidor.
func subdirIn(root, sub string) (string, error) {
	rt, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer rt.Close()
	if sub != "" {
		if err := rt.MkdirAll(sub, 0o755); err != nil {
			return "", err
		}
	}
	return filepath.Join(root, sub), nil
}

// probe escribe y borra un fichero, para saber que la cuenta tiene permiso de escritura.
func probe(dir, folder string) error {
	p := filepath.Join(dir, ".teslapi-test")
	if err := os.WriteFile(p, []byte("ok"), 0o644); err != nil {
		return msg.Wrap("nas.not_writable", err, "folder", folder)
	}
	return os.Remove(p)
}

func (l *Local) Mount(_ context.Context, c config.SMB) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(l.Path, 0o755); err != nil {
		return "", err
	}
	d, err := subdirIn(l.Path, c.Subdir)
	if err != nil {
		return "", err
	}
	l.mu.Lock()
	l.dir, l.mounted = d, true
	l.mu.Unlock()
	return d, nil
}

func (l *Local) Unmount(context.Context) error {
	l.mu.Lock()
	l.mounted = false
	l.mu.Unlock()
	return nil
}

func (l *Local) Dir() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dir, l.mounted
}
