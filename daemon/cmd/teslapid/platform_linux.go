//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"teslapi/internal/api"
	"teslapi/internal/gadget"
	"teslapi/internal/nas"
	"teslapi/internal/presence"
	"teslapi/internal/shell"
	"teslapi/internal/snapshot"
	"teslapi/internal/sysupdate"
	"teslapi/internal/wifi"
)

func newPlatform(o opts) (platform, error) {
	// bluetoothd puede no tener el adaptador listo en los primeros segundos del arranque.
	var ble *presence.BlueZ
	var err error
	for i := 0; i < 15; i++ {
		if ble, err = presence.NewBlueZ("hci0"); err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return platform{}, err
	}
	return platform{
		scanner: ble,
		wifi:    &wifi.NM{Iface: "wlan0", Dir: "/data/state/nm"},
		gadget:  &gadget.ConfigFS{Dir: "/sys/kernel/config/usb_gadget/teslapi"},
		snap: &snapshot.Reflink{Image: o.image, SnapDir: o.snapDir,
			MountDir: filepath.Join(o.runDir, "mnt"), IdleAfter: 60 * time.Second},
		nas:        &nas.CIFS{MountPoint: filepath.Join(o.runDir, "nas"), CredFile: filepath.Join(o.runDir, "smb.cred")},
		reboot:     api.SystemReboot,
		poweroff:   api.SystemPoweroff,
		updates:    sysupdate.ExecRunner{Outside: true},
		writeBoot:  writeBoot,
		removeBoot: removeBoot,
		macSuffix:  macSuffix,
	}, nil
}

// writeBoot: la partición de arranque va en solo lectura; se abre solo para escribir.
func writeBoot(path string, b []byte) error {
	dir := filepath.Dir(path)
	if _, err := shell.Run(context.Background(), "mount", "-o", "remount,rw", dir); err != nil {
		return err
	}
	defer shell.Run(context.Background(), "mount", "-o", "remount,ro", dir)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	shell.Run(context.Background(), "sync")
	return nil
}

// removeBoot borra un fichero de la partición de arranque.
func removeBoot(path string) error {
	dir := filepath.Dir(path)
	if _, err := shell.Run(context.Background(), "mount", "-o", "remount,rw", dir); err != nil {
		return err
	}
	defer shell.Run(context.Background(), "mount", "-o", "remount,ro", dir)
	if err := os.Remove(path); err != nil {
		return err
	}
	shell.Run(context.Background(), "sync")
	return nil
}

func macSuffix() string {
	b, err := os.ReadFile("/sys/class/net/wlan0/address")
	if err != nil {
		return "0000"
	}
	h := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(string(b)), ":", ""))
	if len(h) < 4 {
		return "0000"
	}
	return h[len(h)-4:]
}
