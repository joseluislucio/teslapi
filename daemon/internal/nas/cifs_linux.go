//go:build linux

package nas

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"teslapi/internal/config"
	"teslapi/internal/msg"
	"teslapi/internal/shell"
)

// CIFS monta con el cliente SMB del kernel. Las credenciales van en un fichero de
// /run (tmpfs, 600) que solo existe mientras dura mount, y no en la línea de órdenes,
// que se ve en ps.
type CIFS struct {
	MountPoint string // /run/teslapi/nas
	CredFile   string // /run/teslapi/smb.cred

	mu  sync.Mutex
	dir string
}

func (c *CIFS) Mount(ctx context.Context, s config.SMB) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dir != "" && mounted(c.MountPoint) {
		return c.dir, nil
	}
	dir, err := mountAt(ctx, s, c.MountPoint, c.CredFile)
	if err != nil {
		return "", err
	}
	c.dir = dir
	return dir, nil
}

// Test monta en un punto aparte, escribe un fichero de prueba y desmonta.
func (c *CIFS) Test(ctx context.Context, s config.SMB) error {
	mp, cred := c.MountPoint+"-test", c.CredFile+".test"
	dir, err := mountAt(ctx, s, mp, cred)
	if err != nil {
		return err
	}
	defer shell.Run(context.Background(), "umount", "-l", mp)
	return probe(dir, s.Subdir)
}

func mountAt(ctx context.Context, s config.SMB, mp, credFile string) (string, error) {
	if s.Share == "" {
		return "", msg.Err("nas.not_configured")
	}
	// Se repite aquí lo que ya valida la API: la configuración puede venir de un fichero.
	if err := s.Validate(); err != nil {
		return "", err
	}
	if err := Reachable(ctx, s.Share, 5*time.Second); err != nil {
		return "", err
	}
	cred := fmt.Sprintf("username=%s\npassword=%s\n", s.User, s.Password)
	if s.Domain != "" {
		cred += "domain=" + s.Domain + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(credFile), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(credFile, []byte(cred), 0o600); err != nil {
		return "", err
	}
	defer os.Remove(credFile)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return "", err
	}
	if mounted(mp) {
		shell.Run(ctx, "umount", "-l", mp)
	}
	vers := s.Version
	if vers == "" {
		vers = "3.1.1"
	}
	list := []string{
		"credentials=" + credFile, "vers=" + vers, "uid=0", "gid=0",
		"file_mode=0644", "dir_mode=0755", "cache=strict", "noserverino", "iocharset=utf8",
		"echo_interval=10", "soft", "actimeo=30", "nosuid", "nodev", "noexec",
	}
	if s.Encrypt {
		list = append(list, "seal")
	}
	opts := strings.Join(list, ",")
	share := strings.ReplaceAll(s.Share, `\`, "/")
	if _, err := shell.Run(ctx, "mount", "-t", "cifs", share, mp, "-o", opts); err != nil {
		return "", mountErr(err)
	}
	dir, err := subdirIn(mp, s.Subdir)
	if err != nil {
		shell.Run(context.Background(), "umount", "-l", mp)
		return "", msg.Wrap("nas.not_writable", err, "folder", s.Subdir)
	}
	return dir, nil
}

// mountErr traduce los fallos típicos de mount.cifs a algo que se pueda explicar.
func mountErr(err error) error {
	t := err.Error()
	switch {
	case strings.Contains(t, "Permission denied") || strings.Contains(t, "error(13)"):
		return msg.Wrap("nas.auth_failed", err)
	case strings.Contains(t, "No such file or directory") || strings.Contains(t, "error(2)"):
		return msg.Wrap("nas.share_not_found", err)
	}
	return msg.Wrap("nas.mount_failed", err)
}

func (c *CIFS) Unmount(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dir = ""
	if !mounted(c.MountPoint) {
		return nil
	}
	// -l: si la Wi-Fi ya se ha caído, un umount normal se quedaría colgado.
	_, err := shell.Run(ctx, "umount", "-l", c.MountPoint)
	return err
}

func (c *CIFS) Dir() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dir, c.dir != "" && mounted(c.MountPoint)
}

func mounted(mp string) bool {
	b, _ := os.ReadFile("/proc/mounts")
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) > 1 && f[1] == mp {
			return true
		}
	}
	return false
}
