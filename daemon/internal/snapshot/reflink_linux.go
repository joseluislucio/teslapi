//go:build linux

package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"teslapi/internal/diskimg"
	"teslapi/internal/shell"
)

type Reflink struct {
	Image    string // /data/cam_disk.bin
	SnapDir  string // /data/snap (mismo FS XFS que la imagen: requisito del reflink)
	MountDir string // /run/teslapi/mnt
	// IdleAfter: sin escrituras del coche en este tiempo, se considera que no graba.
	IdleAfter time.Duration
}

func (r *Reflink) Snapshot(ctx context.Context) (*Mounted, error) {
	st, err := os.Stat(r.Image)
	if err != nil {
		return nil, err
	}
	idle := time.Since(st.ModTime()) > r.IdleAfter
	// Lo que el coche ha escrito y aún está en la caché de páginas debe llegar al
	// disco antes de clonar los bloques.
	if f, err := os.Open(r.Image); err == nil {
		f.Sync()
		f.Close()
	}
	if err := os.MkdirAll(r.SnapDir, 0o700); err != nil {
		return nil, err
	}
	name := "snap-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	snap := filepath.Join(r.SnapDir, name+".bin")
	if _, err := shell.Run(ctx, "cp", "--reflink=always", r.Image, snap); err != nil {
		os.Remove(snap)
		return nil, err
	}
	m, err := r.mount(ctx, snap, name, true)
	if err != nil {
		os.Remove(snap)
		return nil, err
	}
	m.CarIdle = idle
	inner := m.release
	m.release = func() error {
		err := inner()
		return errors.Join(err, os.Remove(snap))
	}
	return m, nil
}

func (r *Reflink) Live(ctx context.Context) (*Mounted, error) {
	return r.mount(ctx, r.Image, "live", false)
}

func (r *Reflink) mount(ctx context.Context, file, name string, ro bool) (*Mounted, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	st, _ := f.Stat()
	vol, err := diskimg.Detect(f, st.Size())
	f.Close()
	if err != nil {
		return nil, err
	}
	out, err := shell.Run(ctx, "losetup", "--find", "--show",
		"--offset", strconv.FormatInt(vol.Offset, 10), "--sizelimit", strconv.FormatInt(vol.Size, 10), file)
	if err != nil {
		return nil, err
	}
	loop := strings.TrimSpace(out)
	detach := func() error { _, err := shell.Run(context.Background(), "losetup", "-d", loop); return err }

	// Si el coche estaba escribiendo cuando se hizo la copia, el FS de la copia puede
	// quedar "sucio". Se repara la copia, nunca el original que usa el coche.
	repaired, err := repair(ctx, loop, vol.FSType)
	if err != nil {
		detach()
		return nil, err
	}
	mp := filepath.Join(r.MountDir, name)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		detach()
		return nil, err
	}
	opts := "noatime,nosuid,nodev,noexec,uid=0,gid=0,fmask=0022,dmask=0022"
	if ro {
		opts = "ro," + opts
	}
	if vol.FSType == "vfat" {
		opts += ",iocharset=utf8,shortname=mixed"
	}
	if _, err := shell.Run(ctx, "mount", "-t", vol.FSType, "-o", opts, loop, mp); err != nil {
		detach()
		return nil, err
	}
	var sfs unix.Statfs_t
	unix.Statfs(mp, &sfs)
	return &Mounted{
		Root: mp, Vol: vol, Taken: time.Now(), Repaired: repaired,
		FreeBytes: int64(sfs.Bavail) * sfs.Bsize, TotalBytes: int64(sfs.Blocks) * sfs.Bsize,
		release: func() error {
			_, err := shell.Run(context.Background(), "umount", mp)
			if err != nil {
				_, err = shell.Run(context.Background(), "umount", "-l", mp)
			}
			os.Remove(mp)
			return errors.Join(err, detach())
		},
	}, nil
}

// repair devuelve true si el fsck corrigió algo. Códigos: 0 = limpio, 1 = corregido.
func repair(ctx context.Context, dev, fstype string) (bool, error) {
	var err error
	switch fstype {
	case "exfat":
		_, err = shell.Run(ctx, "fsck.exfat", "-y", dev)
	case "vfat":
		_, err = shell.Run(ctx, "fsck.vfat", "-a", "-w", dev)
	default:
		return false, fmt.Errorf("unsupported filesystem %q", fstype)
	}
	switch shell.ExitCode(err) {
	case -1:
		if err != nil {
			return false, err
		}
		return false, nil
	case 0:
		return false, nil
	case 1:
		return true, nil
	}
	return false, err
}

func (r *Reflink) Cleanup(ctx context.Context) error {
	var errs []error
	mounts, _ := os.ReadFile("/proc/mounts")
	for _, l := range strings.Split(string(mounts), "\n") {
		f := strings.Fields(l)
		if len(f) > 1 && strings.HasPrefix(f[1], r.MountDir+"/") {
			if _, err := shell.Run(ctx, "umount", "-l", f[1]); err != nil {
				errs = append(errs, err)
			}
		}
	}
	snaps, _ := filepath.Glob(filepath.Join(r.SnapDir, "snap-*.bin"))
	for _, s := range append(snaps, r.Image) {
		out, _ := shell.Run(ctx, "losetup", "-j", s)
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if dev, _, ok := strings.Cut(l, ":"); ok {
				shell.Run(ctx, "losetup", "-d", dev)
			}
		}
	}
	for _, s := range snaps {
		if err := os.Remove(s); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
