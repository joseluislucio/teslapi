// teslapid es el daemon de teslapi: pendrive para el coche, archivado al NAS al
// detectar la casa y portal web en la LAN.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"teslapi/internal/api"
	"teslapi/internal/archive"
	"teslapi/internal/auth"
	"teslapi/internal/bootcfg"
	"teslapi/internal/config"
	"teslapi/internal/core"
	"teslapi/internal/gadget"
	"teslapi/internal/nas"
	"teslapi/internal/presence"
	"teslapi/internal/sdwear"
	"teslapi/internal/snapshot"
	"teslapi/internal/sysinfo"
	"teslapi/internal/sysupdate"
	"teslapi/internal/wifi"
)

var version = "dev"

type opts struct {
	state, image, snapDir, runDir, bootFile, listen, tlsListen, allowHosts, dev string
}

type platform struct {
	scanner  presence.Scanner
	wifi     wifi.Controller
	gadget   gadget.Controller
	snap     snapshot.Snapshotter
	nas      nas.Mounter
	reboot   func() error
	poweroff func() error
	updates  sysupdate.Runner
	// devCharger simula ver el cargador (solo en compilaciones de desarrollo).
	devCharger func(c config.Charger, present bool)
	// writeBoot reescribe un fichero de la partición de arranque (va montada en solo lectura).
	writeBoot func(path string, b []byte) error
	// removeBoot borra un fichero de esa misma partición.
	removeBoot func(path string) error
	// macSuffix: cuatro caracteres del adaptador Wi-Fi, para el nombre de la red del asistente.
	macSuffix func() string
}

func main() {
	var o opts
	flag.StringVar(&o.state, "state", "/data/state", "directorio de estado (config, índice)")
	flag.StringVar(&o.image, "image", "/data/cam_disk.bin", "imagen del pendrive")
	flag.StringVar(&o.snapDir, "snapdir", "/data/snap", "snapshots (mismo FS XFS que la imagen)")
	flag.StringVar(&o.runDir, "run", "/run/teslapi", "montajes y ficheros temporales")
	flag.StringVar(&o.bootFile, "bootcfg", "/boot/firmware/teslapi.txt", "fichero de configuración opcional de la partición de arranque")
	flag.StringVar(&o.listen, "listen", ":80", "dirección del portal")
	flag.StringVar(&o.tlsListen, "tls-listen", ":443", "dirección HTTPS, si existen tls/cert.pem y tls/key.pem en el directorio de estado")
	flag.StringVar(&o.allowHosts, "allow-host", "", "nombres extra con los que se accede al portal, separados por comas")
	flag.StringVar(&o.dev, "dev", "", "modo desarrollo: carpeta con TeslaCam/ de ejemplo (solo con -tags dev)")
	flag.Parse()

	ring := core.NewRing(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log := slog.New(ring)

	var p platform
	var err error
	if o.dev != "" {
		p, err = devPlatform(&o)
	} else {
		p, err = newPlatform(o)
	}
	if err != nil {
		log.Error("inicializar plataforma", "err", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(o.state, 0o700); err != nil {
		log.Error("estado", "err", err)
		os.Exit(1)
	}
	cfg, err := config.Open(filepath.Join(o.state, "config.json"))
	if err != nil {
		log.Error("configuración", "err", err)
		os.Exit(1)
	}
	ix, err := archive.OpenIndex(filepath.Join(o.state, "teslapi.db"))
	if err != nil {
		log.Error("índice", "err", err)
		os.Exit(1)
	}
	defer ix.Close()

	key := filepath.Join(o.state, "session.key")
	imp := bootcfg.Importer{Path: o.bootFile, StateFile: filepath.Join(o.state, "bootcfg.sum"), Store: cfg,
		HashPassword: auth.Hash, WriteBoot: p.writeBoot}
	if r, done, err := imp.Run(); err != nil {
		log.Error("teslapi.txt", "err", err)
	} else if done {
		log.Info("teslapi.txt imported", "settings", strings.Join(r.Applied, ","), "unknown", strings.Join(r.Unknown, ","))
	}
	apName := "TeslaPi"
	if p.macSuffix != nil {
		apName += "-" + p.macSuffix()
	}

	sys := &sysinfo.Reader{BootTimesFile: filepath.Join(o.state, "boot_times"), DataDir: filepath.Dir(o.image),
		ThermalFile: filepath.Join(o.state, "thermal.json"), HeartbeatFile: filepath.Join(o.state, "heartbeat.json"), Version: version, Fake: o.dev != ""}
	sd := &sdwear.Reader{StateFile: filepath.Join(o.state, "sdwear.json"), Fake: o.dev != "",
		Params: func() sdwear.Params {
			c := cfg.Get().SDCard.Resolved()
			return sdwear.Params{PECycles: c.PECycles, WAF: c.WAF}
		}}
	cacheDir := filepath.Join(filepath.Dir(o.image), "cache", "apt")
	if o.dev != "" {
		cacheDir = filepath.Join(o.dev, "cache", "apt")
	}
	updates := sysupdate.New(p.updates, cacheDir, filepath.Join(o.state, "update_rates.json"))
	updates.FreeBytes = sysinfo.FreeBytes
	if o.dev != "" {
		updates.FreeBytes = func(string) uint64 { return 50e9 }
		updates.RebootFile = filepath.Join(o.state, "reboot-required")
	}
	mon := presence.NewMonitor(p.scanner, func() config.Charger { return cfg.Get().Charger })
	d := core.New(core.Deps{
		Cfg: cfg, Index: ix, Presence: mon, WiFi: p.wifi, Gadget: p.gadget, Snap: p.snap, NAS: p.nas,
		Sys: sys,
		Log: log, Logs: ring, Dev: o.dev != "", APSSID: apName,
		Rescue:    func() bool { return bootcfg.Rescue(o.bootFile) },
		PublishAP: func(ssid, pw string) error { return publishAP(p, o.bootFile, ssid, pw) },
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var hosts []string
	if o.allowHosts != "" {
		hosts = strings.Split(o.allowHosts, ",")
	}
	server := &api.Server{D: d, Updates: updates, SD: sd, AllowedHosts: hosts, Log: log,
		Reboot: p.reboot, Poweroff: p.poweroff, Sessions: &auth.Sessions{KeyFile: key}}
	if p.devCharger != nil {
		server.DevCharger = func(present bool) { p.devCharger(cfg.Get().Charger, present) }
	}
	handler := server.Handler()
	newServer := func(addr string, h http.Handler) *http.Server {
		// Sin WriteTimeout: los vídeos y /api/stream duran lo que duran.
		return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute,
			IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10}
	}
	// HTTPS opcional: basta con dejar un certificado en <estado>/tls (ver SECURITY.md).
	certFile, keyFile := filepath.Join(o.state, "tls", "cert.pem"), filepath.Join(o.state, "tls", "key.pem")
	plain := handler
	var tlsSrv *http.Server
	if _, cerr := os.Stat(certFile); cerr == nil {
		if _, kerr := os.Stat(keyFile); kerr == nil {
			tlsSrv = newServer(o.tlsListen, handler)
			plain = server.RedirectHTTPS(handler, func() bool { return cfg.Get().Setup.Done })
			go func() {
				if err := tlsSrv.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
					log.Error("portal https", "err", err)
				}
			}()
		}
	}
	srv := newServer(o.listen, plain)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("portal", "err", err)
		}
	}()
	log.Info("teslapid arrancado", "version", version, "portal", o.listen, "dev", o.dev != "")
	updates.Busy, updates.Online = d.Busy, d.Online
	if o.dev != "" {
		updates.Online = func() bool { return true } // en desarrollo el Wi-Fi falso no cuenta
	}
	go sys.Run(ctx)
	go sd.Run(ctx.Done())
	d.Run(ctx)
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
	if tlsSrv != nil {
		tlsSrv.Shutdown(sctx)
	}
}

// publishAP deja la contraseña generada de la red del asistente en la partición de arranque,
// junto a teslapi.txt. Con ssid vacío retira la nota.
func publishAP(p platform, bootFile, ssid, pw string) error {
	note := filepath.Join(filepath.Dir(bootFile), "teslapi-setup-network.txt")
	if ssid == "" {
		if _, err := os.Stat(note); err != nil {
			return nil
		}
		if p.removeBoot != nil {
			return p.removeBoot(note)
		}
		return nil
	}
	if p.writeBoot == nil {
		return nil
	}
	return p.writeBoot(note, []byte("teslapi setup network / red de configuracion\r\n\r\nNetwork: "+ssid+"\r\nPassword: "+pw+"\r\n"))
}
