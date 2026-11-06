package core

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"teslapi/internal/archive"
	"teslapi/internal/config"
	"teslapi/internal/diskimg"
	"teslapi/internal/gadget"
	"teslapi/internal/nas"
	"teslapi/internal/presence"
	"teslapi/internal/snapshot"
	"teslapi/internal/sysinfo"
	"teslapi/internal/wifi"
)

// snap simula el snapshot sobre una carpeta, con espacio libre configurable.
type snap struct {
	root        string
	total, free int64
	lives       atomic.Int32
}

var testVol = diskimg.Volume{FSType: "exfat", Serial: 0xCAFE, Offset: 1 << 20}

func (s *snap) Snapshot(context.Context) (*snapshot.Mounted, error) {
	return snapshot.NewMounted(s.root, testVol, s.total, s.free, true, func() error { return nil }), nil
}
func (s *snap) Live(context.Context) (*snapshot.Mounted, error) {
	s.lives.Add(1)
	return snapshot.NewMounted(s.root, testVol, s.total, s.free, true, func() error { return nil }), nil
}
func (s *snap) Cleanup(context.Context) error { return nil }

// gad cuenta las desconexiones para comprobar que siempre se vuelve a conectar.
type gad struct {
	gadget.Fake
	detaches, attaches atomic.Int32
}

func (g *gad) Detach() error { g.detaches.Add(1); return g.Fake.Detach() }
func (g *gad) Attach() error { g.attaches.Add(1); return g.Fake.Attach() }

func put(t *testing.T, root, rel string, size int) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T, free int64) (*Daemon, *presence.Fake, *wifi.Fake, *gad, *snap, string) {
	t.Helper()
	drive, nasDir, state := t.TempDir(), t.TempDir(), t.TempDir()
	for _, cam := range []string{"front", "back"} {
		put(t, drive, "TeslaCam/SavedClips/2025-03-01_10-00-00/2025-03-01_09-59-00-"+cam+".mp4", 200_000)
		put(t, drive, "TeslaCam/SentryClips/2025-03-02_10-00-00/2025-03-02_09-59-00-"+cam+".mp4", 200_000)
		put(t, drive, "TeslaCam/RecentClips/2025-03-29_12-00-00-"+cam+".mp4", 200_000)
	}
	cfg, _ := config.Open(filepath.Join(state, "config.json"))
	c := config.Defaults()
	c.Charger = config.Charger{Address: "AA:BB:CC:DD:EE:01", RSSIMin: -90, SeenWindows: 1, AbsentAfterSec: 1, ScanOnSec: 1, ScanPeriodSec: 1}
	c.WiFi = config.WiFi{SSID: "garaje", Password: "x"}
	c.SMB.Share = "//nas/tesla"
	c.Setup.Done = true
	cfg.Set(c)
	ix, err := archive.OpenIndex(filepath.Join(state, "ix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	ble := &presence.Fake{}
	wf := &wifi.Fake{}
	g := &gad{}
	sn := &snap{root: drive, total: 1 << 30, free: free}
	log := slog.New(NewRing(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	d := New(Deps{
		Cfg: cfg, Index: ix, Presence: presence.NewMonitor(ble, func() config.Charger { return cfg.Get().Charger }),
		WiFi: wf, Gadget: g, Snap: sn, NAS: &nas.Local{Path: nasDir}, Sys: &sysinfo.Reader{Fake: true}, Log: log,
		Logs: NewRing(slog.NewTextHandler(os.Stderr, nil)), APSSID: "TeslaPi-TEST",
	})
	return d, ble, wf, g, sn, nasDir
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout esperando: %s", what)
}

func TestChargerCycleArchivesAndTurnsWiFiOff(t *testing.T) {
	d, ble, wf, g, _, nasDir := setup(t, 900<<20) // mucho espacio libre: no se borra nada
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	if err := d.RequestArchive(); err == nil {
		t.Fatal("sin Wi-Fi no debe aceptarse un archivado manual")
	}
	time.Sleep(1500 * time.Millisecond)
	if wf.Status(ctx).Enabled {
		t.Fatal("sin cargador la Wi-Fi debe seguir apagada")
	}

	ble.Set(&presence.Sighting{Address: "aa:bb:cc:dd:ee:01", RSSI: -60})
	waitFor(t, "archivado completo", 10*time.Second, func() bool { return d.Status(ctx).Archive.Phase == "done" })
	if !wf.Status(ctx).Enabled {
		t.Fatal("con el cargador la Wi-Fi debe estar encendida")
	}
	for _, rel := range []string{
		"SavedClips/2025-03-01_10-00-00/2025-03-01_09-59-00-front.mp4",
		"SentryClips/2025-03-02_10-00-00/2025-03-02_09-59-00-back.mp4",
		"RecentClips/2025-03-29/2025-03-29_12-00-00-front.mp4",
	} {
		st, err := os.Stat(filepath.Join(nasDir, "TeslaCam", rel))
		if err != nil || st.Size() != 200_000 {
			t.Fatalf("%s no está en el NAS: %v", rel, err)
		}
	}
	st := d.Status(ctx)
	if st.Archive.LastRun == nil || st.Archive.LastRun.Files != 6 || st.Archive.LastRun.Result != "ok" {
		t.Fatalf("historial: %+v", st.Archive.LastRun)
	}
	if g.detaches.Load() != 0 {
		t.Fatal("con espacio de sobra no debe desconectarse el pendrive")
	}
	evs, err := d.Events("pi")
	if err != nil || len(evs) != 3 {
		t.Fatalf("eventos pi: %v %v", len(evs), err)
	}
	for _, e := range evs {
		if !e.Archived {
			t.Fatalf("%s debería figurar como archivado", e.Dir)
		}
	}

	ble.Set(nil)
	waitFor(t, "Wi-Fi apagada al irse", 10*time.Second, func() bool { return !wf.Status(ctx).Enabled })
	if _, ok := d.NAS.Dir(); ok {
		t.Fatal("el NAS debe desmontarse al apagar la Wi-Fi")
	}
	if _, err := d.Events("pi"); err == nil {
		t.Fatal("sin Wi-Fi se libera el snapshot de navegación")
	}
}

func TestLowSpaceFreesOnlyArchivedEventsAndReattaches(t *testing.T) {
	d, ble, _, g, sn, _ := setup(t, 50<<20) // 5 % libre < 20 %
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	ble.Set(&presence.Sighting{Address: "AA:BB:CC:DD:EE:01", RSSI: -60})
	waitFor(t, "archivado", 10*time.Second, func() bool { return d.Status(ctx).Archive.Phase == "done" })
	if g.detaches.Load() != 1 || g.attaches.Load() != 1 || !g.Status().Attached {
		t.Fatalf("detach=%d attach=%d attached=%v", g.detaches.Load(), g.attaches.Load(), g.Status().Attached)
	}
	if sn.lives.Load() != 1 {
		t.Fatal("debía montar la imagen viva una vez")
	}
	for _, rel := range []string{"TeslaCam/SavedClips/2025-03-01_10-00-00", "TeslaCam/SentryClips/2025-03-02_10-00-00"} {
		if _, err := os.Stat(filepath.Join(sn.root, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s debía borrarse del pendrive", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(sn.root, "TeslaCam/RecentClips/2025-03-29_12-00-00-front.mp4")); err != nil {
		t.Fatal("RecentClips nunca se borra: lo gestiona el coche")
	}
	if d.Status(ctx).Archive.LastRun.Freed != 800_000 {
		t.Fatalf("freed=%d", d.Status(ctx).Archive.LastRun.Freed)
	}
}

// La Wi-Fi puede venir encendida del arranque (estado en tmpfs): el daemon la apaga.
func TestWiFiForcedOffAtStartup(t *testing.T) {
	d, _, wf, _, _, _ := setup(t, 900<<20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wf.Up(ctx, "garaje", "x")
	go d.Run(ctx)
	waitFor(t, "Wi-Fi apagada al arrancar", 3*time.Second, func() bool { return !wf.Status(ctx).Enabled })
}

func TestNASUnmountsWhenIdleAndRemountsOnDemand(t *testing.T) {
	old := NASIdle
	NASIdle = 400 * time.Millisecond
	defer func() { NASIdle = old }()
	d, ble, _, _, _, _ := setup(t, 900<<20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	ble.Set(&presence.Sighting{Address: "aa:bb:cc:dd:ee:01", RSSI: -60})
	waitFor(t, "archivado completo", 10*time.Second, func() bool { return d.Status(ctx).Archive.Phase == "done" })
	waitFor(t, "NAS desmontado por inactividad", 25*time.Second, func() bool { _, ok := d.NAS.Dir(); return !ok })

	// Con la Wi-Fi de casa, mirar el NAS desde el portal lo vuelve a montar.
	if _, err := d.Events("nas"); err != nil {
		t.Fatalf("eventos del NAS tras desmontar: %v", err)
	}
	if _, ok := d.NAS.Dir(); !ok {
		t.Fatal("ver el NAS debe montarlo de nuevo")
	}
}

func TestJoinHomeFallsBackToSecondWiFi(t *testing.T) {
	d, _, wf, _, _, _ := setup(t, 900<<20)
	wf.Reject = func(ssid, _ string) error {
		if ssid == "garaje" {
			return wifi.ErrNoNetwork
		}
		return nil
	}
	cfg := d.Cfg.Get()
	cfg.WiFi.Extra = []config.WiFiNet{{SSID: "casa", Password: "clave-casa-1"}}
	if !d.joinHome(context.Background(), cfg) {
		t.Fatal("debe conectar a la segunda red")
	}
	if st := wf.Status(context.Background()); !st.Connected || st.SSID != "casa" {
		t.Fatalf("estado: %+v", st)
	}
	// Si no hay ninguna, falla y deja el error de la principal.
	wf.Reject = func(string, string) error { return wifi.ErrNoNetwork }
	if d.joinHome(context.Background(), cfg) {
		t.Fatal("sin ninguna red no debe dar éxito")
	}
}
