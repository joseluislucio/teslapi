// Package sysupdate comprueba y aplica las actualizaciones del sistema de la Pi (software
// y firmware) sin tocar la raíz, que es de solo lectura: las listas y los .deb van a /data
// y la raíz solo se remonta en escritura durante la instalación.
package sysupdate

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Group string

const (
	Software Group = "software"
	Firmware Group = "firmware"
)

// Pkg es un paquete que cambia de versión.
type Pkg struct {
	Name string `json:"name"`
	From string `json:"from,omitempty"` // vacío = paquete nuevo
	To   string `json:"to"`
	Size int64  `json:"size"` // bytes a descargar
}

// Plan resume lo que se aplicaría con una de las dos acciones.
type Plan struct {
	Group         Group    `json:"group"`
	Packages      []Pkg    `json:"packages"`
	New           int      `json:"new"`
	Removals      []string `json:"removals,omitempty"`
	DownloadBytes int64    `json:"download_bytes"`
	DiskDelta     int64    `json:"disk_delta"` // bytes de disco en la raíz (negativo = libera)
	DownloadSec   int      `json:"download_sec"`
	InstallSec    int      `json:"install_sec"`
	ETASec        int      `json:"eta_sec"`
	// Notes son avisos que el portal traduce: reboot | services.
	Notes []string `json:"notes,omitempty"`
}

// firmwareRe reconoce lo que escribe en /boot/firmware o /lib/firmware: el firmware de la
// GPU y los chips de radio, el kernel, sus cabeceras y el cargador de arranque.
var firmwareRe = regexp.MustCompile(`^(raspi-firmware|rpi-eeprom|rpi-update|bluez-firmware|firmware-.+|linux-image-.+|linux-headers-.+|linux-kbuild-.+|linux-base-.+|linux-libc-dev|raspberrypi-(bootloader|kernel).*)$`)

// servicesRe: paquetes cuya instalación reinicia servicios que usa la Pi (red, SSH, Bluetooth).
var servicesRe = regexp.MustCompile(`^(network-manager|wpasupplicant|openssh-.+|bluez|avahi-.+|systemd|libc6|dbus.*|libssl.+|openssl)$`)

// Classify dice a qué botón pertenece un paquete.
func Classify(name string) Group {
	if firmwareRe.MatchString(name) {
		return Firmware
	}
	return Software
}

var instRe = regexp.MustCompile(`^Inst (\S+) (?:\[([^\]]+)\] )?\((\S+) `)

// parseSim lee la salida de `apt-get -s`: qué paquetes se instalan o actualizan y cuáles se quitan.
func parseSim(out string) (pkgs []Pkg, removals []string) {
	for _, l := range strings.Split(out, "\n") {
		if m := instRe.FindStringSubmatch(l); m != nil {
			pkgs = append(pkgs, Pkg{Name: strings.SplitN(m[1], ":", 2)[0], From: m[2], To: m[3]})
		} else if strings.HasPrefix(l, "Remv ") {
			if f := strings.Fields(l); len(f) > 1 {
				removals = append(removals, strings.SplitN(f[1], ":", 2)[0])
			}
		}
	}
	return
}

// parseURIs lee `apt-get --print-uris`: tamaño de cada .deb por paquete y el balance de disco.
func parseURIs(out string) (sizes map[string]int64, disk int64) {
	sizes = map[string]int64{}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "'") {
			f := strings.Fields(l)
			if len(f) >= 3 {
				n, _ := strconv.ParseInt(f[2], 10, 64)
				sizes[strings.SplitN(f[1], "_", 2)[0]] += n
			}
			continue
		}
		if i := strings.Index(l, "After this operation, "); i >= 0 {
			disk = parseAptSize(l[i+len("After this operation, "):])
			if strings.Contains(l, "will be freed") {
				disk = -disk
			}
		}
	}
	return
}

// parseAptSize lee "17.7 MB" / "512 kB" (apt usa unidades decimales).
func parseAptSize(s string) int64 {
	f := strings.Fields(s)
	if len(f) < 2 {
		return 0
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(f[0], ",", ""), 64)
	if err != nil {
		return 0
	}
	switch strings.ToLower(f[1]) {
	case "kb":
		v *= 1e3
	case "mb":
		v *= 1e6
	case "gb":
		v *= 1e9
	}
	return int64(v)
}

// Rates son las velocidades con las que se estima el tiempo; se afinan con cada ejecución.
type Rates struct {
	DownBps   float64 `json:"down_bps"`    // bytes/s de descarga
	SecPerPkg float64 `json:"sec_per_pkg"` // segundos de instalación por paquete
}

func DefaultRates() Rates { return Rates{DownBps: 1.7e6, SecPerPkg: 4} }

// buildPlan junta la simulación, los tamaños y las velocidades en un plan.
func buildPlan(g Group, sim, uris string, rt Rates) Plan {
	pkgs, rem := parseSim(sim)
	sizes, disk := parseURIs(uris)
	p := Plan{Group: g, Packages: []Pkg{}, Removals: rem, DiskDelta: disk}
	reboot, services := g == Firmware, false
	for _, k := range pkgs {
		k.Size = sizes[k.Name]
		p.DownloadBytes += k.Size
		if k.From == "" {
			p.New++
		}
		if Classify(k.Name) == Firmware {
			reboot = true
		}
		if servicesRe.MatchString(k.Name) {
			services = true
		}
		if k.Name == "libc6" || k.Name == "systemd" {
			reboot = true
		}
		p.Packages = append(p.Packages, k)
	}
	sort.Slice(p.Packages, func(i, j int) bool { return p.Packages[i].Name < p.Packages[j].Name })
	if reboot {
		p.Notes = append(p.Notes, "reboot")
	}
	if services {
		p.Notes = append(p.Notes, "services")
	}
	if len(p.Removals) > 0 {
		p.Notes = append(p.Notes, "removals")
	}
	p.estimate(rt)
	return p
}

// estimate calcula el tiempo: descarga a la velocidad medida e instalación por paquete
// (el firmware y el kernel tardan más en el arranque: initramfs, copia a /boot).
func (p *Plan) estimate(rt Rates) {
	if rt.DownBps <= 0 || rt.SecPerPkg <= 0 {
		rt = DefaultRates()
	}
	if len(p.Packages) == 0 {
		p.DownloadSec, p.InstallSec, p.ETASec = 0, 0, 0
		return
	}
	p.DownloadSec = int(float64(p.DownloadBytes)/rt.DownBps + 0.5)
	p.InstallSec = 6 + int(float64(len(p.Packages))*rt.SecPerPkg+0.5)
	if p.Group == Firmware {
		p.InstallSec += 30
	}
	p.ETASec = p.DownloadSec + p.InstallSec
}

func (p Plan) names() []string {
	n := make([]string, len(p.Packages))
	for i, k := range p.Packages {
		n[i] = k.Name
	}
	return n
}

// progress es la línea de estado que apt escribe en el descriptor -o APT::Status-Fd.
type progress struct {
	Kind string // dlstatus | pmstatus | pmerror
	Name string
	Pct  float64
	Text string
}

func parseStatus(l string) (progress, bool) {
	f := strings.SplitN(strings.TrimSpace(l), ":", 4)
	if len(f) < 3 {
		return progress{}, false
	}
	switch f[0] {
	case "dlstatus", "pmstatus", "pmerror":
	default:
		return progress{}, false
	}
	pct, err := strconv.ParseFloat(f[2], 64)
	if err != nil {
		return progress{}, false
	}
	p := progress{Kind: f[0], Name: f[1], Pct: pct}
	if len(f) == 4 {
		p.Text = f[3]
	}
	return p, true
}
