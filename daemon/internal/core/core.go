// Package core es el cerebro de teslapid: decide cuándo encender la Wi-Fi,
// cuándo archivar y cuándo liberar espacio, y publica el estado para el portal.
package core

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"teslapi/internal/archive"
	"teslapi/internal/config"
	"teslapi/internal/diskimg"
	"teslapi/internal/gadget"
	"teslapi/internal/msg"
	"teslapi/internal/nas"
	"teslapi/internal/presence"
	"teslapi/internal/snapshot"
	"teslapi/internal/sysinfo"
	"teslapi/internal/teslacam"
	"teslapi/internal/wifi"
)

type Deps struct {
	Cfg      *config.Store
	Index    *archive.Index
	Presence *presence.Monitor
	WiFi     wifi.Controller
	Gadget   gadget.Controller
	Snap     snapshot.Snapshotter
	NAS      nas.Mounter
	Sys      *sysinfo.Reader
	Log      *slog.Logger
	Logs     *Ring
	// Rescue: teslapi.txt pide la Wi-Fi siempre encendida (force_wifi = 1).
	Rescue func() bool
	// APSSID: nombre de la red propia del asistente (TeslaPi-XXXX).
	APSSID string
	// PublishAP deja la contraseña de esa red donde el dueño pueda leerla (partición de
	// arranque); vacía, la retira. Solo hace falta si no vino de teslapi.txt.
	PublishAP func(ssid, password string) error
	Dev       bool
}

type Usage struct {
	Category string `json:"category"`
	Bytes    int64  `json:"bytes"`
	Files    int    `json:"files"`
}

type Drive struct {
	Gadget     gadget.Status  `json:"gadget"`
	Volume     diskimg.Volume `json:"volume"`
	TotalBytes int64          `json:"total_bytes"`
	FreeBytes  int64          `json:"free_bytes"`
	Usage      []Usage        `json:"usage"`
	SnapshotAt time.Time      `json:"snapshot_at"`
	CarIdle    bool           `json:"car_idle"`
	Known      bool           `json:"known"` // hay un snapshot leído (si no, los datos no son fiables)
}

type ArchiveState struct {
	Phase      string       `json:"phase"` // idle | connecting | snapshot | copying | verifying | freeing | done | interrupted | error
	Message    msg.Msg      `json:"message"`
	Started    time.Time    `json:"started"`
	TotalFiles int64        `json:"total_files"`
	DoneFiles  int64        `json:"done_files"`
	TotalBytes int64        `json:"total_bytes"`
	DoneBytes  int64        `json:"done_bytes"`
	Current    string       `json:"current,omitempty"`
	SpeedBps   float64      `json:"speed_bps"`
	ETASec     float64      `json:"eta_sec"`
	Pending    int          `json:"pending"`
	LastRun    *archive.Run `json:"last_run,omitempty"`
}

type Status struct {
	Now  time.Time `json:"now"`
	Mode string    `json:"mode"` // away | home | rescue | setup
	// Trigger: cómo detecta la casa (ble | wifi); HomeSetup: si ya está configurado.
	Trigger   string         `json:"trigger"`
	HomeSetup bool           `json:"home_setup"`
	Charger   presence.State `json:"charger"`
	WiFi      wifi.Status    `json:"wifi"`
	WiFiError *msg.Msg       `json:"wifi_error,omitempty"`
	Drive     Drive          `json:"drive"`
	Archive   ArchiveState   `json:"archive"`
	System    sysinfo.Info   `json:"system"`
	NAS       bool           `json:"nas"`
	NASSetup  bool           `json:"nas_setup"`
	SetupDone bool           `json:"setup_done"`
	Dev       bool           `json:"dev"`
}

// SetupState es lo que necesita el asistente.
type SetupState struct {
	// Step: wifi (la Pi emite su red y espera la Wi-Fi de casa) | joining (probando la
	// Wi-Fi de casa) | lan (ya está en la red de casa: el resto se configura desde ahí) |
	// done.
	Step     string         `json:"step"`
	APSSID   string         `json:"ap_ssid"`
	Networks []wifi.Network `json:"networks"`
	WiFiSSID string         `json:"wifi_ssid"`
	IP       string         `json:"ip,omitempty"`
	Error    *msg.Msg       `json:"error,omitempty"`
}

type Daemon struct {
	Deps

	trigger chan struct{}

	mu        sync.Mutex
	wifiUp    bool
	standby   bool // modo Wi-Fi: radio encendida esperando a ver la red de casa
	apUp      bool
	apSeen    time.Time // último uso del asistente con la red propia emitida
	apClosed  bool      // la red propia se apagó por falta de uso: no vuelve hasta reiniciar
	joinFail  *msg.Msg  // el asistente no pudo unirse a la Wi-Fi de casa: vuelve a su red
	networks  []wifi.Network
	wifiErr   *msg.Msg
	archiving bool
	cancel    context.CancelFunc
	manual    bool
	lastRunAt time.Time
	phase     string
	message   msg.Msg
	progress  *archive.Progress
	pending   int
	browse    *snapshot.Mounted
	files     []teslacam.File
	drive     Drive
	nasCache  nasListing
	nasUsed   time.Time // último uso del NAS por el portal (para desmontarlo al quedar ocioso)

	wifiMu   sync.Mutex
	wifiAt   time.Time
	wifiLast wifi.Status
}

type nasListing struct {
	at     time.Time
	events []teslacam.Event
}

func New(d Deps) *Daemon {
	return &Daemon{Deps: d, trigger: make(chan struct{}, 1), phase: "idle"}
}

func (d *Daemon) poke() {
	select {
	case d.trigger <- struct{}{}:
	default:
	}
}

func (d *Daemon) Run(ctx context.Context) {
	if err := d.Snap.Cleanup(ctx); err != nil {
		d.Log.Warn("clean up old snapshots", "err", err)
	}
	// El estado de la radio no persiste (tmpfs) y NetworkManager arranca con la Wi-Fi
	// encendida: se apaga explícitamente para que el estado real coincida con wifiUp=false.
	if !d.rescue() {
		if err := d.WiFi.Down(ctx); err != nil {
			d.Log.Warn("wifi off at startup", "err", err)
		}
	}
	go d.Presence.Run(ctx)
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	d.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			d.shutdown()
			return
		case p := <-d.Presence.Changes():
			if p {
				d.Log.Info("home device in range")
			} else {
				d.Log.Info("home device out of range")
			}
			d.reconcile(ctx)
		case <-d.trigger:
			d.reconcile(ctx)
		case <-tick.C:
			d.reconcile(ctx)
		}
	}
}

func (d *Daemon) rescue() bool { return d.Rescue != nil && d.Rescue() }

// atHome dice si el coche está en casa según el modo configurado.
func (d *Daemon) atHome(ctx context.Context, cfg config.Config) bool {
	if cfg.Trigger == config.TriggerWiFi {
		st := d.WiFi.Status(ctx)
		return st.Connected && !st.AP && cfg.WiFi.Has(st.SSID)
	}
	return d.Presence.State().Present
}

func (d *Daemon) reconcile(ctx context.Context) {
	defer d.idleUnmountNAS(ctx)
	cfg := d.Cfg.Get()
	if !cfg.Setup.Done {
		d.reconcileSetup(ctx, cfg)
		return
	}
	d.stopHotspot(ctx)
	if cfg.Trigger == config.TriggerWiFi {
		d.reconcileWiFiMode(ctx, cfg)
	} else {
		d.reconcileBLEMode(ctx, cfg)
	}
}

// reconcileBLEMode: la Wi-Fi solo se enciende con el dispositivo de casa a la vista.
func (d *Daemon) reconcileBLEMode(ctx context.Context, cfg config.Config) {
	present := d.Presence.State().Present
	// Sin forma de saber que está en casa, la Wi-Fi queda encendida para poder
	// terminar la configuración desde el portal.
	want := present || d.rescue() || !cfg.HomeConfigured()

	d.mu.Lock()
	if d.standby {
		// Venía del modo Wi-Fi: se parte de la radio apagada.
		d.standby, d.wifiUp = false, false
	}
	up := d.wifiUp
	d.mu.Unlock()

	switch {
	case want && !up:
		if !d.joinHome(ctx, cfg) {
			return
		}
	case !want && up:
		d.Log.Info("turning wifi off")
		d.leaveHome(ctx)
		if err := d.WiFi.Down(ctx); err != nil {
			d.Log.Warn("wifi off", "err", err)
		}
		return
	}
	d.maybeArchive(ctx, cfg, present)
}

// reconcileWiFiMode: la radio queda en espera y NetworkManager se conecta solo al ver
// la red de casa; estar conectado a ella es estar en casa.
func (d *Daemon) reconcileWiFiMode(ctx context.Context, cfg config.Config) {
	d.mu.Lock()
	standby := d.standby
	d.mu.Unlock()
	if !standby {
		for _, n := range cfg.WiFi.Networks() {
			if err := d.WiFi.Standby(ctx, n.SSID, n.Password); err != nil {
				d.Log.Error("wifi standby failed", "ssid", n.SSID, "err", err)
				d.setWiFiErr(msg.New("wifi.join_failed", "ssid", n.SSID).WithDetail(err))
				return
			}
		}
		d.mu.Lock()
		d.standby, d.wifiErr = true, nil
		d.mu.Unlock()
	}
	present := d.atHome(ctx, cfg)
	d.mu.Lock()
	was := d.wifiUp
	d.wifiUp = present
	d.mu.Unlock()
	if was && !present {
		d.Log.Info("home wifi lost")
		d.leaveHome(ctx)
		return
	}
	if !was && present {
		d.Log.Info("home wifi connected", "ssid", cfg.WiFi.SSID)
	}
	d.maybeArchive(ctx, cfg, present)
}

// leaveHome corta lo que dependa de la red antes de perderla.
func (d *Daemon) leaveHome(ctx context.Context) {
	d.stopArchive()
	d.releaseBrowse()
	d.NAS.Unmount(ctx)
	d.mu.Lock()
	d.wifiUp, d.manual = false, false
	d.mu.Unlock()
}

func (d *Daemon) maybeArchive(ctx context.Context, cfg config.Config, present bool) {
	d.mu.Lock()
	up, archiving, manual, lastRun := d.wifiUp, d.archiving, d.manual, d.lastRunAt
	d.mu.Unlock()
	if !up || archiving || cfg.SMB.Share == "" {
		return
	}
	due := lastRun.IsZero() || time.Since(lastRun) > time.Duration(cfg.Archive.RepeatMin)*time.Minute
	if manual || (present && due) {
		d.startArchive(ctx)
	}
}

// joinHome conecta a la Wi-Fi de casa y deja el error a la vista si no puede.
func (d *Daemon) joinHome(ctx context.Context, cfg config.Config) bool {
	if cfg.WiFi.SSID == "" {
		d.setWiFiErr(msg.New("wifi.not_configured"))
		return false
	}
	// Se prueban las redes por orden; el error que se muestra es el de la principal.
	var first *msg.Msg
	for _, n := range cfg.WiFi.Networks() {
		d.Log.Info("turning wifi on", "ssid", n.SSID)
		wctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := d.WiFi.Up(wctx, n.SSID, n.Password)
		cancel()
		if err == nil {
			d.mu.Lock()
			d.wifiUp, d.wifiErr = true, nil
			d.mu.Unlock()
			return true
		}
		d.Log.Error("wifi connect failed", "ssid", n.SSID, "err", err)
		if first == nil {
			m := msg.New("wifi.join_failed", "ssid", n.SSID).WithDetail(err)
			first = &m
		}
		if ctx.Err() != nil {
			break
		}
	}
	d.setWiFiErr(*first)
	return false
}

// reconcileSetup: mientras el asistente no termina, la Pi o bien está en la Wi-Fi de
// casa (y el resto se configura desde la LAN) o bien emite su propia red para que el
// móvil se conecte y le diga cuál es la Wi-Fi de casa.
func (d *Daemon) reconcileSetup(ctx context.Context, cfg config.Config) {
	d.mu.Lock()
	up, ap, failed := d.wifiUp, d.apUp, d.joinFail != nil
	d.mu.Unlock()
	if cfg.WiFi.SSID != "" && !failed {
		if up {
			return
		}
		d.stopHotspot(ctx)
		ap = false
		if d.joinHome(ctx, cfg) {
			return
		}
		d.mu.Lock()
		d.joinFail = d.wifiErr
		d.mu.Unlock()
	}
	if ap {
		d.mu.Lock()
		idle := time.Since(d.apSeen) > SetupAPWindow
		d.mu.Unlock()
		if idle {
			d.Log.Warn("setup network idle, turning it off until the next boot")
			d.stopHotspot(ctx)
			if err := d.WiFi.Down(ctx); err != nil {
				d.Log.Warn("wifi off", "err", err)
			}
			d.mu.Lock()
			d.apClosed = true
			d.mu.Unlock()
		}
		return
	}
	d.mu.Lock()
	closed := d.apClosed
	d.mu.Unlock()
	if closed {
		return
	}
	pw, err := d.apPassword(cfg)
	if err != nil {
		d.Log.Error("setup network password", "err", err)
		d.setWiFiErr(msg.New("wifi.hotspot_failed").WithDetail(err))
		return
	}
	// Antes de emitir se mira qué redes hay: con el hotspot activo no se puede.
	if ns, err := d.WiFi.Scan(ctx); err == nil {
		d.mu.Lock()
		d.networks = ns
		d.mu.Unlock()
	} else {
		d.Log.Warn("wifi scan", "err", err)
	}
	d.Log.Info("starting setup network", "ssid", d.APSSID)
	if err := d.WiFi.Hotspot(ctx, d.APSSID, pw); err != nil {
		d.Log.Error("setup network failed", "err", err)
		d.setWiFiErr(msg.New("wifi.hotspot_failed").WithDetail(err))
		return
	}
	d.mu.Lock()
	d.apUp, d.wifiUp, d.apSeen = true, false, time.Now()
	d.mu.Unlock()
}

// SetupAPWindow: sin usar el asistente durante este tiempo se apaga su red, para que no se
// quede emitiendo en la calle. Vuelve en el siguiente arranque.
var SetupAPWindow = 15 * time.Minute

// apPassword devuelve la contraseña de la red del asistente. Por ella viaja la clave de la
// Wi-Fi de casa, así que nunca es abierta: si no vino de teslapi.txt se genera una y se deja
// en la partición de arranque, que solo puede leer quien tiene la tarjeta en la mano.
func (d *Daemon) apPassword(cfg config.Config) (string, error) {
	pw := cfg.Setup.APPassword
	if pw != "" {
		return pw, nil
	}
	pw = rand.Text()[:16]
	if _, err := d.Cfg.Update(func(c *config.Config) { c.Setup.APPassword = pw }); err != nil {
		return "", err
	}
	if d.PublishAP != nil {
		if err := d.PublishAP(d.APSSID, pw); err != nil {
			return "", err
		}
	}
	return pw, nil
}

func (d *Daemon) stopHotspot(ctx context.Context) {
	d.mu.Lock()
	ap := d.apUp
	d.apUp = false
	d.mu.Unlock()
	if ap {
		d.Log.Info("stopping setup network")
		d.WiFi.StopHotspot(ctx)
	}
}

// SetupWiFi guarda la Wi-Fi de casa elegida en el asistente. La Pi deja su red (tras
// un momento, para que la respuesta llegue al móvil) e intenta unirse; si no puede,
// vuelve a emitir su red con el motivo.
func (d *Daemon) SetupWiFi(ssid, password, language string) error {
	if d.Cfg.Get().Setup.Done {
		return msg.Err("setup.already_done")
	}
	if ssid == "" {
		return msg.Err("setup.ssid_required")
	}
	if password != "" && len(password) < 8 {
		return msg.Err("setup.wifi_password_short")
	}
	if _, err := d.Cfg.Update(func(c *config.Config) {
		c.WiFi = config.WiFi{SSID: ssid, Password: password}
		if language != "" {
			c.Language = language
		}
	}); err != nil {
		return err
	}
	d.mu.Lock()
	d.joinFail, d.wifiErr, d.wifiUp = nil, nil, false
	d.mu.Unlock()
	go func() {
		time.Sleep(SetupSwitchDelay)
		d.poke()
	}()
	return nil
}

// SetupSwitchDelay: margen para que la respuesta llegue al móvil antes de cortar la red.
var SetupSwitchDelay = 2 * time.Second

// FinishSetup marca el asistente como terminado.
func (d *Daemon) FinishSetup(language, passwordHash string) error {
	_, err := d.Cfg.Update(func(c *config.Config) {
		c.Setup.Done = true
		c.Setup.APPassword = "" // ya no se emite; si el asistente vuelve, se genera otra
		if language != "" {
			c.Language = language
		}
		if passwordHash != "" {
			c.Portal.PasswordHash = passwordHash
		}
	})
	if d.PublishAP != nil {
		if perr := d.PublishAP("", ""); perr != nil {
			d.Log.Warn("remove setup network note", "err", perr)
		}
	}
	d.poke()
	return err
}

// RestartSetup vuelve a abrir el asistente (la Wi-Fi de casa se conserva).
func (d *Daemon) RestartSetup() error {
	_, err := d.Cfg.Update(func(c *config.Config) { c.Setup.Done = false })
	d.poke()
	return err
}

func (d *Daemon) Setup(ctx context.Context) SetupState {
	cfg := d.Cfg.Get()
	st := SetupState{APSSID: d.APSSID, WiFiSSID: cfg.WiFi.SSID}
	w := d.cachedWiFi(ctx)
	d.mu.Lock()
	if d.apUp {
		d.apSeen = time.Now() // el asistente sondea esto: alguien lo está usando
	}
	st.Networks, st.Error = d.networks, d.joinFail
	up, ap := d.wifiUp, d.apUp
	d.mu.Unlock()
	if st.Networks == nil {
		st.Networks = []wifi.Network{}
	}
	switch {
	case cfg.Setup.Done:
		st.Step = "done"
	case up:
		st.Step, st.IP = "lan", w.IP
	case cfg.WiFi.SSID != "" && st.Error == nil && !ap:
		st.Step = "joining"
	default:
		st.Step = "wifi"
	}
	return st
}

func (d *Daemon) setWiFiErr(m msg.Msg) {
	d.mu.Lock()
	d.wifiErr = &m
	d.mu.Unlock()
}

// RequestArchive lanza un archivado desde el portal (con la Wi-Fi ya encendida).
// Busy dice por qué ahora no se debe tocar el sistema (vacío = se puede).
func (d *Daemon) Busy() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.archiving {
		return "archiving"
	}
	return ""
}

// Online dice si la Pi tiene red para descargar (conectada a una Wi-Fi, no emitiendo la suya).
func (d *Daemon) Online() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st := d.WiFi.Status(ctx)
	return st.Connected && !st.AP
}

func (d *Daemon) RequestArchive() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.wifiUp {
		return msg.Err("archive.wifi_off")
	}
	if d.archiving {
		return msg.Err("archive.running")
	}
	if d.Cfg.Get().SMB.Share == "" {
		return msg.Err("nas.not_configured")
	}
	d.manual = true
	d.poke()
	return nil
}

func (d *Daemon) startArchive(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	d.mu.Lock()
	d.archiving, d.cancel, d.manual = true, cancel, false
	d.mu.Unlock()
	go func() {
		defer cancel()
		run := d.runArchive(ctx)
		// Primero el historial y luego la fase final: quien vea "done" ya encuentra el ciclo.
		if err := d.Index.AddRun(run); err != nil {
			d.Log.Warn("save history", "err", err)
		}
		d.setPhase(finalPhase(run.Result), run.Message)
		d.mu.Lock()
		d.archiving, d.cancel, d.lastRunAt = false, nil, time.Now()
		d.mu.Unlock()
	}()
}

func finalPhase(result string) string {
	switch result {
	case "ok":
		return "done"
	case "interrupted":
		return "interrupted"
	}
	return "error"
}

func (d *Daemon) stopArchive() {
	d.mu.Lock()
	c := d.cancel
	d.mu.Unlock()
	if c == nil {
		return
	}
	c()
	for i := 0; i < 100; i++ {
		d.mu.Lock()
		a := d.archiving
		d.mu.Unlock()
		if !a {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (d *Daemon) setPhase(p string, m msg.Msg) {
	d.mu.Lock()
	d.phase, d.message = p, m
	d.mu.Unlock()
	if !m.IsZero() {
		d.Log.Info("archive: "+m.String(), "phase", p)
	}
}

func (d *Daemon) runArchive(ctx context.Context) (run archive.Run) {
	cfg := d.Cfg.Get()
	run.Start = time.Now()
	defer func() {
		run.End = time.Now()
		switch {
		case ctx.Err() != nil:
			run.Result = "interrupted"
			run.Message = msg.New("archive.interrupted")
		case run.Result == "error":
		default:
			run.Result = "ok"
			m := msg.New("archive.done", "files", run.Files, "bytes", run.Bytes)
			if run.Files == 0 && run.LeftOver == 0 {
				m = msg.New("archive.nothing_new")
			}
			if run.LeftOver > 0 {
				m = msg.New("archive.partial", "files", run.Files, "pending", run.LeftOver)
			}
			run.Message = m
		}
	}()
	fail := func(stage string, err error) archive.Run {
		run.Result = "error"
		// Un fallo que ya trae código (NAS inalcanzable, credenciales…) se explica mejor que la etapa.
		var me *msg.Error
		if errors.As(err, &me) {
			run.Message = me.Msg
			run.Message.Args = mergeArgs(run.Message.Args, "stage", stage)
		} else {
			run.Message = msg.New("archive.failed", "stage", stage).WithDetail(err)
		}
		return run
	}

	d.setPhase("connecting", msg.New("archive.connecting"))
	var dst string
	var err error
	// La red acaba de levantarse: DHCP/ARP pueden tardar unos segundos.
	for i := 0; i < 12; i++ {
		if dst, err = d.NAS.Mount(ctx, cfg.SMB); err == nil || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	if err != nil {
		return fail("nas", err)
	}

	d.setPhase("snapshot", msg.New("archive.reading"))
	snap, err := d.Snap.Snapshot(ctx)
	if err != nil {
		return fail("snapshot", err)
	}
	files, err := teslacam.Walk(snap.Root)
	if err != nil {
		snap.Release()
		return fail("read", err)
	}
	vol := snap.Vol.ID()
	run.Volume = vol
	if err := d.Index.PruneVolumes(vol); err != nil {
		d.Log.Warn("prune index", "err", err)
	}
	d.setBrowse(snap, files)

	opts := archive.PlanOptions{
		RecentClips: cfg.Archive.RecentClips, MinClipBytes: cfg.Archive.MinClipBytes,
		CarIdle: snap.CarIdle, Settle: time.Duration(cfg.Archive.SettleSec) * time.Second,
	}
	items := archive.Plan(files, d.Index, vol, opts)
	p := &archive.Progress{TotalFiles: int64(len(items)), Started: time.Now()}
	for _, f := range items {
		p.TotalBytes += f.Size
	}
	run.Pending = len(items)
	d.mu.Lock()
	d.progress, d.pending = p, len(items)
	d.mu.Unlock()

	d.Presence.SetTransferring(true)
	d.setPhase("copying", msg.New("archive.copying", "files", len(items), "bytes", p.TotalBytes))
	res, err := archive.Copy(ctx, snap.Root, dst, items, d.Index, vol, p)
	d.Presence.SetTransferring(false)
	run.Files, run.Bytes = res.Files, res.Bytes
	run.LeftOver = len(items) - int(p.DoneFiles.Load())
	d.mu.Lock()
	d.pending = run.LeftOver
	d.nasCache = nasListing{}
	d.mu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return run
		}
		return fail("copy", err)
	}

	if snap.TotalBytes > 0 && snap.CarIdle && snap.FreeBytes*100/snap.TotalBytes < int64(cfg.Archive.FreeSpacePct) {
		freed, err := d.freeSpace(ctx, files, vol, snap, cfg.Archive.FreeSpacePct, dst)
		run.Freed = freed
		if err != nil {
			return fail("free", err)
		}
	}
	return run
}

// freeSpace desconecta el pendrive unos segundos, borra eventos ya verificados en el
// NAS y lo vuelve a conectar. Es el único momento en que el coche deja de verlo.
func (d *Daemon) freeSpace(ctx context.Context, files []teslacam.File, vol string, snap *snapshot.Mounted, pct int, dst string) (int64, error) {
	target := snap.TotalBytes * int64(pct+10) / 100
	want := target - snap.FreeBytes
	rels, freed := archive.Deletable(files, d.Index, vol, want)
	if len(rels) == 0 {
		d.Log.Warn("drive almost full and nothing archived that can be deleted")
		return 0, nil
	}
	// Lo que se borra aquí solo queda en el NAS: se compara antes con lo que hay allí.
	d.setPhase("verifying", msg.New("archive.verifying", "bytes", freed, "files", len(rels)))
	rels, freed, err := archive.Verify(ctx, snap.Root, dst, files, rels, d.Index, vol)
	if err != nil {
		return 0, err
	}
	if len(rels) == 0 {
		d.Log.Warn("archived copies differ from the drive: nothing deleted, they will be copied again")
		return 0, nil
	}
	d.setPhase("freeing", msg.New("archive.freeing", "bytes", freed, "files", len(rels)))
	if err := d.Gadget.Detach(); err != nil {
		return 0, err
	}
	defer func() {
		if err := d.Gadget.Attach(); err != nil {
			d.Log.Error("could NOT reattach the drive to the car", "err", err)
		}
	}()
	live, err := d.Snap.Live(ctx)
	if err != nil {
		return 0, err
	}
	defer live.Release()
	if live.Vol.ID() != vol {
		return 0, msg.Err("archive.drive_changed", "now", live.Vol.ID(), "was", vol)
	}
	dirs := map[string]bool{}
	for _, r := range rels {
		if err := os.Remove(filepath.Join(live.Root, filepath.FromSlash(r))); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		dirs[path.Dir(r)] = true
	}
	for dir := range dirs {
		os.Remove(filepath.Join(live.Root, filepath.FromSlash(dir))) // solo si quedó vacía
	}
	if err := d.Index.Forget(vol, rels); err != nil {
		return freed, err
	}
	return freed, nil
}

func (d *Daemon) setBrowse(s *snapshot.Mounted, files []teslacam.File) {
	usage := map[string]*Usage{}
	for _, f := range files {
		u := usage[f.Category]
		if u == nil {
			u = &Usage{Category: f.Category}
			usage[f.Category] = u
		}
		u.Bytes += f.Size
		u.Files++
	}
	us := []Usage{}
	for _, u := range usage {
		us = append(us, *u)
	}
	sort.Slice(us, func(i, j int) bool { return us[i].Bytes > us[j].Bytes })
	d.mu.Lock()
	old := d.browse
	d.browse, d.files = s, files
	d.drive = Drive{Volume: s.Vol, TotalBytes: s.TotalBytes, FreeBytes: s.FreeBytes, Usage: us,
		SnapshotAt: s.Taken, CarIdle: s.CarIdle, Known: true}
	d.mu.Unlock()
	if old != nil && old != s {
		old.Release()
	}
}

func (d *Daemon) releaseBrowse() {
	d.mu.Lock()
	old := d.browse
	d.browse, d.files = nil, nil
	d.mu.Unlock()
	old.Release()
}

// RefreshBrowse hace un snapshot nuevo solo para ver los clips en el portal.
func (d *Daemon) RefreshBrowse(ctx context.Context) error {
	d.mu.Lock()
	busy := d.archiving
	d.mu.Unlock()
	if busy {
		return msg.Err("refresh.busy")
	}
	s, err := d.Snap.Snapshot(ctx)
	if err != nil {
		return err
	}
	files, err := teslacam.Walk(s.Root)
	if err != nil {
		s.Release()
		return err
	}
	d.setBrowse(s, files)
	return nil
}

// NASIdle: tiempo sin archivar ni mirar el NAS tras el que se desmonta. Un recurso SMB
// montado y ocioso acaba cortado por el servidor (CIFS "has not responded") y no sirve de nada.
var NASIdle = 5 * time.Minute

// nasDir da la carpeta del NAS para el portal, montándola si hay Wi-Fi de casa y no lo está.
func (d *Daemon) nasDir() (string, bool) {
	dir, ok := d.NAS.Dir()
	if !ok {
		cfg := d.Cfg.Get()
		d.mu.Lock()
		up := d.wifiUp
		d.mu.Unlock()
		if !up || cfg.SMB.Share == "" {
			return "", false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var err error
		if dir, err = d.NAS.Mount(ctx, cfg.SMB); err != nil {
			d.Log.Warn("nas mount for browsing", "err", err)
			return "", false
		}
	}
	d.mu.Lock()
	d.nasUsed = time.Now()
	d.mu.Unlock()
	return dir, true
}

// idleUnmountNAS desmonta el NAS cuando no se archiva ni se mira desde hace NASIdle.
func (d *Daemon) idleUnmountNAS(ctx context.Context) {
	if _, ok := d.NAS.Dir(); !ok {
		return
	}
	d.mu.Lock()
	last, busy := d.nasUsed, d.archiving
	if d.lastRunAt.After(last) {
		last = d.lastRunAt
	}
	d.mu.Unlock()
	if busy || time.Since(last) < NASIdle {
		return
	}
	d.Log.Info("nas idle, unmounting")
	if err := d.NAS.Unmount(ctx); err != nil {
		d.Log.Warn("nas unmount", "err", err)
	}
}

func (d *Daemon) shutdown() {
	d.stopArchive()
	d.releaseBrowse()
	d.NAS.Unmount(context.Background())
}

// StatusTTL: cuánto vale la lectura de la Wi-Fi para el portal. Leerla lanza nmcli e iw:
// sin esto, cada navegador abierto añadiría procesos a una Pi que está grabando.
var StatusTTL = 2 * time.Second

func (d *Daemon) cachedWiFi(ctx context.Context) wifi.Status {
	d.wifiMu.Lock()
	defer d.wifiMu.Unlock()
	if !d.wifiAt.IsZero() && time.Since(d.wifiAt) < StatusTTL {
		return d.wifiLast
	}
	d.wifiLast, d.wifiAt = d.WiFi.Status(ctx), time.Now()
	return d.wifiLast
}

func (d *Daemon) Status(ctx context.Context) Status {
	cfg := d.Cfg.Get()
	st := Status{Now: time.Now(), Charger: d.Presence.State(), Dev: d.Dev, Trigger: cfg.Trigger,
		HomeSetup: cfg.HomeConfigured(), NASSetup: cfg.SMB.Share != "", SetupDone: cfg.Setup.Done}
	st.WiFi = d.cachedWiFi(ctx)
	st.System = d.Sys.Read()
	_, st.NAS = d.NAS.Dir()
	d.mu.Lock()
	defer d.mu.Unlock()
	st.WiFiError = d.wifiErr
	st.Drive = d.drive
	st.Drive.Gadget = d.Gadget.Status()
	home := st.Charger.Present
	if cfg.Trigger == config.TriggerWiFi {
		home = st.WiFi.Connected && !st.WiFi.AP && cfg.WiFi.Has(st.WiFi.SSID)
	}
	switch {
	case !cfg.Setup.Done:
		st.Mode = "setup"
	case d.rescue():
		st.Mode = "rescue"
	case home:
		st.Mode = "home"
	default:
		st.Mode = "away"
	}
	a := ArchiveState{Phase: d.phase, Message: d.message, Pending: d.pending}
	if p := d.progress; p != nil {
		a.Started, a.TotalFiles, a.TotalBytes = p.Started, p.TotalFiles, p.TotalBytes
		a.DoneFiles, a.DoneBytes = p.DoneFiles.Load(), p.DoneBytes.Load()
		if c, ok := p.Current.Load().(string); ok {
			a.Current = c
		}
		if el := time.Since(p.Started).Seconds(); el > 1 && d.archiving {
			a.SpeedBps = float64(a.DoneBytes) / el
			if a.SpeedBps > 0 {
				a.ETASec = float64(a.TotalBytes-a.DoneBytes) / a.SpeedBps
			}
		}
	}
	if h := d.Index.History(1); len(h) > 0 {
		a.LastRun = &h[0]
	}
	st.Archive = a
	return st
}

// EventView añade a un evento si ya está a salvo en el NAS.
type EventView struct {
	teslacam.Event
	Source   string `json:"source"`
	Archived bool   `json:"archived"`
}

// Events lista los eventos del pendrive (último snapshot) o del NAS.
func (d *Daemon) Events(source string) ([]EventView, error) {
	switch source {
	case "pi":
		d.mu.Lock()
		b, files := d.browse, d.files
		d.mu.Unlock()
		if b == nil {
			return nil, errNoSnapshot
		}
		vol := b.Vol.ID()
		has := map[string]bool{}
		for _, f := range files {
			has[f.Rel] = d.Index.Has(vol, f.Rel, f.Size)
		}
		var out []EventView
		for _, e := range teslacam.Events(b.Root, files) {
			ok := len(e.Segments) > 0
			for _, s := range e.Segments {
				for _, rel := range s.Cameras {
					ok = ok && has[rel]
				}
			}
			out = append(out, EventView{Event: e, Source: "pi", Archived: ok})
		}
		return out, nil
	case "nas":
		dir, ok := d.nasDir()
		if !ok {
			return nil, errNoNAS
		}
		d.mu.Lock()
		c := d.nasCache
		d.mu.Unlock()
		if c.events == nil || time.Since(c.at) > 5*time.Minute {
			files, err := teslacam.WalkUnder(dir, "TeslaCam/")
			if err != nil {
				return nil, err
			}
			c = nasListing{at: time.Now(), events: teslacam.Events(filepath.Dir(dir), files)}
			// Events lee event.json bajo root/TeslaCam/...; en el NAS la carpeta puede
			// llamarse distinto, así que se resuelve con NASPath.
			for i := range c.events {
				if c.events[i].Meta == nil && !c.events[i].Encrypted {
					c.events[i].Meta = teslacam.ReadMeta(d.NASPath(dir, c.events[i].Dir+"/event.json"))
				}
			}
			d.mu.Lock()
			d.nasCache = c
			d.mu.Unlock()
		}
		out := make([]EventView, len(c.events))
		for i, e := range c.events {
			out[i] = EventView{Event: e, Source: "nas", Archived: true}
		}
		return out, nil
	}
	return nil, msg.Err("events.unknown_source", "source", source)
}

var (
	errNoSnapshot = msg.Err("events.no_snapshot")
	errNoNAS      = msg.Err("events.no_nas")
)

func mergeArgs(a map[string]any, kv ...any) map[string]any {
	if a == nil {
		a = map[string]any{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		a[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return a
}

// NASPath traduce una ruta "TeslaCam/..." a la carpeta de destino del NAS.
func (d *Daemon) NASPath(dir, rel string) string {
	r := rel
	if len(r) > len("TeslaCam/") && r[:len("TeslaCam/")] == "TeslaCam/" {
		r = r[len("TeslaCam/"):]
	}
	return filepath.Join(dir, filepath.FromSlash(r))
}

// MediaRoot devuelve la carpeta desde la que se sirven los vídeos de un origen.
// Para "pi" las rutas son relativas a la raíz del volumen; para "nas", a la carpeta TeslaCam.
func (d *Daemon) MediaRoot(source string) (string, bool) {
	switch source {
	case "pi":
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.browse == nil {
			return "", false
		}
		return d.browse.Root, true
	case "nas":
		return d.nasDir()
	}
	return "", false
}
