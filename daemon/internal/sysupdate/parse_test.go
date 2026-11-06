package sysupdate

import (
	"os"
	"strings"
	"testing"
)

// Los ficheros de testdata son salida REAL de apt en la Pi (2026-11-05).
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseRealSimulation(t *testing.T) {
	pkgs, rem := parseSim(fixture(t, "sim.txt"))
	if len(pkgs) != 27 || len(rem) != 0 {
		t.Fatalf("paquetes=%d quitar=%d", len(pkgs), len(rem))
	}
	by := map[string]Pkg{}
	for _, p := range pkgs {
		by[p.Name] = p
	}
	if p := by["curl"]; p.From != "8.14.1-2+deb13u5" || p.To != "8.14.1-2+deb13u7" {
		t.Fatalf("curl: %+v", p)
	}
	if p := by["rpi-eeprom"]; p.From != "28.33-1" || p.To != "28.36-1" {
		t.Fatalf("rpi-eeprom: %+v", p)
	}
	if _, ok := by["apt"]; !ok { // línea con "[apt-utils:arm64 ]" al final
		t.Fatal("falta apt")
	}
}

func TestParseRealURIs(t *testing.T) {
	sizes, disk := parseURIs(fixture(t, "uris.txt"))
	if len(sizes) != 27 {
		t.Fatalf("tamaños=%d", len(sizes))
	}
	var sum int64
	for _, v := range sizes {
		sum += v
	}
	// apt dice "Need to get 35.1 MB"; la suma de los .deb debe coincidir al decimal.
	if sum < 35_000_000 || sum > 35_200_000 {
		t.Fatalf("suma %d", sum)
	}
	if sizes["libcamera0.7"] != 825002 {
		t.Fatalf("libcamera0.7: %d", sizes["libcamera0.7"])
	}
	if disk != 17_700_000 {
		t.Fatalf("disco %d", disk)
	}
}

func TestClassify(t *testing.T) {
	for n, want := range map[string]Group{
		"rpi-eeprom": Firmware, "raspi-firmware": Firmware, "firmware-brcm80211": Firmware, "linux-image-6.12.34+rpt-rpi-v8": Firmware,
		"linux-image-rpi-v8": Firmware, "bluez-firmware": Firmware,
		"linux-base-rpi-2712": Firmware, "linux-base-6.18.60+rpt-rpi-v8": Firmware, "linux-base": Software,
		"curl": Software, "libcamera0.7": Software, "linux-libc-dev": Firmware, "firmware-linux-free": Firmware, "raspi-config": Software,
	} {
		if got := Classify(n); got != want {
			t.Errorf("%s: %s, quería %s", n, got, want)
		}
	}
}

func TestBuildPlanEstimate(t *testing.T) {
	// El botón de software solo lleva lo que no es firmware (rpi-eeprom va en el otro).
	var keep []string
	for _, l := range strings.Split(fixture(t, "sim.txt"), "\n") {
		if !strings.Contains(l, "rpi-eeprom") {
			keep = append(keep, l)
		}
	}
	p := buildPlan(Software, strings.Join(keep, "\n"), fixture(t, "uris.txt"), Rates{DownBps: 1.7e6, SecPerPkg: 4})
	if len(p.Packages) != 26 || p.DownloadBytes < 15e6 {
		t.Fatalf("%d paquetes, %d bytes", len(p.Packages), p.DownloadBytes)
	}
	// ≈ 20 MB a 1,7 MB/s ≈ 12 s de descarga; 26 paquetes ≈ 6 + 104 s.
	if p.DownloadSec < 10 || p.DownloadSec > 14 || p.InstallSec != 6+104 || p.ETASec != p.DownloadSec+p.InstallSec {
		t.Fatalf("estimación: dl=%d inst=%d eta=%d", p.DownloadSec, p.InstallSec, p.ETASec)
	}
	if len(p.Notes) != 0 {
		t.Fatalf("software sin servicios ni firmware no debe avisar: %v", p.Notes)
	}
}

func TestFirmwarePlanNeedsReboot(t *testing.T) {
	sim := "Inst rpi-eeprom [28.33-1] (28.36-1 Raspberry Pi Foundation:stable [all])\n"
	p := buildPlan(Firmware, sim, "'http://x/rpi-eeprom_28.36-1_all.deb' rpi-eeprom_28.36-1_all.deb 5000000 MD5Sum:abc\n", DefaultRates())
	if !contains(p.Notes, "reboot") || p.InstallSec != 6+4+30 {
		t.Fatalf("%+v", p)
	}
	if p.New != 0 {
		t.Fatal("no es nuevo")
	}
}

func TestEmptyPlanHasNoETA(t *testing.T) {
	p := buildPlan(Software, "Reading package lists...\n0 upgraded, 0 newly installed\n", "", DefaultRates())
	if len(p.Packages) != 0 || p.ETASec != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestNewAndRemovedPackages(t *testing.T) {
	sim := "Inst libnuevo (1.0 Debian:13/stable [arm64])\nRemv viejo [2.0]\nInst curl [1] (2 Debian:13 [arm64])\n"
	p := buildPlan(Software, sim, "", DefaultRates())
	if p.New != 1 || len(p.Removals) != 1 || p.Removals[0] != "viejo" || !contains(p.Notes, "removals") {
		t.Fatalf("%+v", p)
	}
}

func TestParseStatusLines(t *testing.T) {
	for _, c := range []struct {
		in   string
		ok   bool
		kind string
		pct  float64
	}{
		{"dlstatus:3:42.5000:Retrieving file 3 of 10", true, "dlstatus", 42.5},
		{"pmstatus:curl:61.2500:Unpacking curl (8.14.1) over (8.14.0) ...", true, "pmstatus", 61.25},
		{"pmerror:curl:10.0:dpkg: error", true, "pmerror", 10},
		{"media-change:x:y:z", false, "", 0},
		{"basura", false, "", 0},
	} {
		p, ok := parseStatus(c.in)
		if ok != c.ok || p.Kind != c.kind || p.Pct != c.pct {
			t.Errorf("%q: %+v ok=%v", c.in, p, ok)
		}
	}
	if p, _ := parseStatus("pmstatus:x:1:a:b: c"); !strings.Contains(p.Text, "a:b") {
		t.Fatal("el texto puede llevar ':'")
	}
}
