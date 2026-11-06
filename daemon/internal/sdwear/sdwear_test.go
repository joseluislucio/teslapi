package sdwear

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// rig monta un sysfs falso con una tarjeta y su partición /data.
type rig struct {
	t    *testing.T
	root string
	r    *Reader
	now  time.Time
}

func newRig(t *testing.T, cid string) *rig {
	t.Helper()
	root := t.TempDir()
	sys := filepath.Join(root, "sys")
	disk := filepath.Join(sys, "devices", "mmc", "mmcblk0")
	os.MkdirAll(filepath.Join(disk, "device"), 0o755)
	os.MkdirAll(filepath.Join(disk, "mmcblk0p3"), 0o755)
	os.MkdirAll(filepath.Join(sys, "block"), 0o755)
	os.MkdirAll(filepath.Join(sys, "class", "block"), 0o755)
	os.Symlink(disk, filepath.Join(sys, "block", "mmcblk0"))
	os.Symlink(filepath.Join(disk, "mmcblk0p3"), filepath.Join(sys, "class", "block", "mmcblk0p3"))
	w := func(rel, v string) { os.WriteFile(filepath.Join(disk, rel), []byte(v+"\n"), 0o644) }
	w("size", strconv.Itoa(250_000_000)) // sectores de 512 B ≈ 128 GB
	w("device/cid", cid)
	w("device/manfid", "0x000003")
	w("device/name", "SA128")
	w("device/date", "09/2026")
	os.WriteFile(filepath.Join(root, "mountinfo"), []byte(
		"27 1 179:3 / /data rw,noatime shared:2 - xfs /dev/mmcblk0p3 rw\n26 1 179:2 / / ro shared:1 - ext4 /dev/root ro\n"), 0o644)
	os.WriteFile(filepath.Join(root, "boot_id"), []byte("boot-A\n"), 0o644)
	rg := &rig{t: t, root: root, now: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
	rg.r = &Reader{StateFile: filepath.Join(root, "sdwear.json"), Sys: sys, MountInfo: filepath.Join(root, "mountinfo"),
		BootIDF: filepath.Join(root, "boot_id"), UptimeF: filepath.Join(root, "uptime-none"), Now: func() time.Time { return rg.now },
		Params: func() Params { return Params{PECycles: 1000, WAF: 2} }}
	return rg
}

// stat escribe los contadores del disco (campos 3=lecturas, 5=escrituras, 7=sectores escritos).
func (g *rig) stat(readSect, wOps, wSect uint64) {
	f := []string{"0", "0", strconv.FormatUint(readSect, 10), "0", strconv.FormatUint(wOps, 10), "0", strconv.FormatUint(wSect, 10), "0", "0", "0", "0"}
	os.WriteFile(filepath.Join(g.r.Sys, "block", "mmcblk0", "stat"), []byte("   "+strings.Join(f, " ")+"\n"), 0o644)
}

func (g *rig) boot(id string) { os.WriteFile(g.r.BootIDF, []byte(id+"\n"), 0o644) }

func TestDeviceFromDataMountAndIdentity(t *testing.T) {
	g := newRig(t, "cid-1")
	g.stat(0, 0, 0)
	g.r.Load()
	in := g.r.Read()
	if in.Device != "mmcblk0" || !in.Present || in.Vendor != "SanDisk" || in.Model != "SA128" {
		t.Fatalf("identidad: %+v", in)
	}
	if in.Capacity != 250_000_000*512 {
		t.Fatalf("capacidad: %d", in.Capacity)
	}
}

func TestCountsAcrossRebootsAndServiceRestarts(t *testing.T) {
	g := newRig(t, "cid-1")
	g.r.Load()
	g.stat(0, 100, 1000)
	g.r.Sample()
	g.stat(0, 250, 3000) // mismo arranque: solo la diferencia
	g.r.Sample()
	if g.r.st.TotalWSect != 3000 || g.r.st.TotalWOps != 250 {
		t.Fatalf("mismo arranque: %+v", g.r.st)
	}
	// El servicio se reinicia dentro del mismo arranque: se recarga y no se duplica.
	g2 := &Reader{StateFile: g.r.StateFile, Sys: g.r.Sys, MountInfo: g.r.MountInfo, BootIDF: g.r.BootIDF, Now: g.r.Now}
	g2.Load()
	g.stat(0, 300, 3500)
	g2.Sample()
	if g2.st.TotalWSect != 3500 || g2.st.TotalWOps != 300 {
		t.Fatalf("reinicio del servicio: %+v", g2.st)
	}
	// Arranque nuevo: los contadores del kernel vuelven a cero y se suman enteros.
	g.boot("boot-B")
	g.stat(0, 40, 800)
	g2.Sample()
	if g2.st.TotalWSect != 3500+800 || g2.st.TotalWOps != 340 {
		t.Fatalf("arranque nuevo: %+v", g2.st)
	}
	// Un contador que baja sin cambiar de arranque se trata como reinicio del contador.
	g.stat(0, 10, 100)
	g2.Sample()
	if g2.st.TotalWSect != 3500+800+100 {
		t.Fatalf("contador a la baja: %d", g2.st.TotalWSect)
	}
}

func TestOtherCardResetsCounters(t *testing.T) {
	g := newRig(t, "cid-1")
	g.r.Load()
	g.stat(0, 10, 5000)
	g.r.Sample()
	// Se cambia la tarjeta (otro CID): el acumulado no es suyo.
	os.WriteFile(filepath.Join(g.r.Sys, "block", "mmcblk0", "device", "cid"), []byte("cid-2\n"), 0o644)
	g.r.Load()
	if g.r.st.TotalWSect != 0 || g.r.st.CardID != "cid-2" {
		t.Fatalf("otra tarjeta debe empezar de cero: %+v", g.r.st)
	}
}

func TestEstimateMath(t *testing.T) {
	g := newRig(t, "cid-1")
	g.r.Load()
	// 10 días midiendo, 50 GB al día.
	g.r.st.Since = g.now.AddDate(0, 0, -10)
	for i := 7; i >= 1; i-- {
		g.r.st.Days = append(g.r.st.Days, DayCount{Date: g.now.AddDate(0, 0, -i).Format("2006-01-02"), Bytes: 50e9, Ops: 864000})
	}
	g.r.st.TotalWSect = uint64(350e9 / 512)
	g.r.st.TotalWOps = 864000 * 7
	g.stat(0, 864000*7, g.r.st.TotalWSect)
	g.r.st.LastWSect, g.r.st.LastWOps, g.r.st.BootID = g.r.st.TotalWSect, g.r.st.TotalWOps, "boot-A"
	in := g.r.Read()
	wantEnd := 250_000_000.0 * 512 * 1000 / 2 // capacidad × ciclos / WAF
	if math.Abs(float64(in.EnduranceBytes)-wantEnd) > 1e6 {
		t.Fatalf("resistencia: %d vs %.0f", in.EnduranceBytes, wantEnd)
	}
	if in.Source != "estimated" || math.Abs(in.UsedPct-350e9/wantEnd*100) > 0.01 {
		t.Fatalf("usado: %+v", in.UsedPct)
	}
	if math.Abs(in.BytesPerDay-350e9/7) > 1e8 {
		t.Fatalf("ritmo: %.0f", in.BytesPerDay)
	}
	if in.WriteIOPS < 9.9 || in.WriteIOPS > 10.1 { // 864000 escrituras / día = 10 por segundo
		t.Fatalf("iops: %.2f", in.WriteIOPS)
	}
	if in.DaysLeft == nil || math.Abs(*in.DaysLeft-(wantEnd-350e9)/(50e9)) > 1 {
		t.Fatalf("días: %v", in.DaysLeft)
	}
	// 350e9 B entre 6.048e6 escrituras ≈ 56,5 KiB por escritura.
	if in.AvgWriteKiB < 55 || in.AvgWriteKiB > 58 {
		t.Fatalf("tamaño medio: %.1f KiB", in.AvgWriteKiB)
	}
}

func TestNoProjectionWithTooLittleHistory(t *testing.T) {
	g := newRig(t, "cid-1")
	g.r.Load()
	g.stat(0, 5, 100)
	in := g.r.Read()
	if in.DaysLeft != nil {
		t.Fatal("con minutos de historia no se debe proyectar la vida")
	}
}

func TestProjectionNeedsAWeekOfHistory(t *testing.T) {
	for _, tc := range []struct {
		days float64
		want bool
	}{{0.3, false}, {3, false}, {6.9, false}, {7.1, true}} {
		g := newRig(t, "cid-1")
		g.r.Load()
		g.r.st.Since = g.now.Add(-time.Duration(tc.days * 24 * float64(time.Hour)))
		g.r.st.Days = []DayCount{{Date: g.now.Format("2006-01-02"), Bytes: 4e9, Ops: 40000}}
		g.r.st.TotalWSect, g.r.st.TotalWOps = uint64(4e9/512), 40000
		g.stat(0, 40000, g.r.st.TotalWSect)
		g.r.st.LastWSect, g.r.st.LastWOps, g.r.st.BootID = g.r.st.TotalWSect, g.r.st.TotalWOps, "boot-A"
		in := g.r.Read()
		if (in.DaysLeft != nil) != tc.want {
			t.Fatalf("%.1f días de historia: DaysLeft=%v, quería presente=%v", tc.days, in.DaysLeft, tc.want)
		}
	}
}

func TestReportedLifeTimeWins(t *testing.T) {
	g := newRig(t, "cid-1")
	g.r.Load()
	g.stat(0, 5, 100)
	dd := filepath.Join(g.r.Sys, "block", "mmcblk0", "device")
	os.WriteFile(filepath.Join(dd, "life_time"), []byte("0x03 0x02\n"), 0o644)
	os.WriteFile(filepath.Join(dd, "pre_eol_info"), []byte("0x01\n"), 0o644)
	in := g.r.Read()
	if in.Source != "reported" || in.ReportedPct == nil || *in.ReportedPct != 30 || in.UsedPct != 30 || in.EOL != "normal" {
		t.Fatalf("lectura real: %+v", in)
	}
}

func TestUnknownVendorShowsHex(t *testing.T) {
	if got := vendorName("0x000042"); got != "0x42" {
		t.Fatal(got)
	}
	if got := vendorName("0x00001b"); got != "Samsung" {
		t.Fatal(got)
	}
}

func TestFakeIsPlausible(t *testing.T) {
	in := (&Reader{Fake: true, Params: func() Params { return Params{PECycles: 1000, WAF: 2} }}).Read()
	if !in.Present || in.DaysLeft == nil || in.UsedPct <= 0 || in.UsedPct >= 100 {
		t.Fatalf("%+v", in)
	}
}

func TestTrackingStartsAtBootNotAtServiceStart(t *testing.T) {
	g := newRig(t, "cid-1")
	os.WriteFile(filepath.Join(g.root, "uptime"), []byte("7200.50 14000.00\n"), 0o644)
	g.r.UptimeF = filepath.Join(g.root, "uptime")
	g.r.Load()
	if want := g.now.Add(-2 * time.Hour); g.r.st.Since.Sub(want) > time.Second || want.Sub(g.r.st.Since) > time.Second {
		t.Fatalf("since=%v quería ≈%v", g.r.st.Since, want)
	}
}
