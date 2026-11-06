package sysinfo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParseThrottled(t *testing.T) {
	for in, want := range map[string]uint32{"throttled=0x0\n": 0, "throttled=0x50005": 0x50005, "throttled=0x80008": 0x80008} {
		got, ok := parseThrottled(in)
		if !ok || got != want {
			t.Fatalf("%q: %x %v", in, got, ok)
		}
	}
	if _, ok := parseThrottled("error=1"); ok {
		t.Fatal("una respuesta sin 'throttled=' no debe dar banderas")
	}
	if _, ok := parseThrottled("throttled=zz"); ok {
		t.Fatal("un valor no hexadecimal no debe dar banderas")
	}
}

// fakeSystem apunta las lecturas a ficheros y a una función controlados por la prueba.
func fakeSystem(t *testing.T, bootID string) (setTemp func(float64), setFlags func(uint32, bool)) {
	t.Helper()
	dir := t.TempDir()
	oldT, oldB, oldR := tempPath, bootIDPath, readThrottle
	t.Cleanup(func() { tempPath, bootIDPath, readThrottle = oldT, oldB, oldR })
	tempPath, bootIDPath = filepath.Join(dir, "temp"), filepath.Join(dir, "boot_id")
	os.WriteFile(bootIDPath, []byte(bootID+"\n"), 0o600)
	var fl uint32
	ok := true
	readThrottle = func() (uint32, bool) { return fl, ok }
	setTemp = func(c float64) { os.WriteFile(tempPath, []byte(strconv.Itoa(int(c*1000))), 0o600) }
	setFlags = func(f uint32, o bool) { fl, ok = f, o }
	return
}

func TestThermalTracksMaxAndFlagsAndPersists(t *testing.T) {
	setTemp, setFlags := fakeSystem(t, "boot-1")
	file := filepath.Join(t.TempDir(), "thermal.json")
	r := &Reader{ThermalFile: file}
	r.loadThermal()

	setTemp(48)
	r.sample()
	setTemp(61.5)
	r.sample()
	setTemp(55)
	r.sample()
	in := r.Read()
	if in.TempMaxC != 61.5 || in.Throttled || in.ThrottledSinceBoot || in.UVSinceBoot {
		t.Fatalf("sin banderas: %+v", in)
	}

	// El firmware avisa de que limitó la CPU y de subtensión desde el arranque.
	setFlags(1<<18|1<<16, true)
	r.sample()
	in = r.Read()
	if !in.ThrottledSinceBoot || !in.UVSinceBoot || in.Undervoltage || in.Throttled {
		t.Fatalf("banderas desde el arranque: %+v", in)
	}
	if n := len(in.Thermal); n != 1 || !in.Thermal[0].Throttled || !in.Thermal[0].Undervolted || in.Thermal[0].MaxTempC != 61.5 {
		t.Fatalf("historial: %+v", in.Thermal)
	}

	// El daemon se reinicia dentro del mismo arranque: sigue el mismo registro, sin duplicar.
	r2 := &Reader{ThermalFile: file}
	r2.loadThermal()
	setTemp(50)
	r2.sample()
	if h := r2.Read().Thermal; len(h) != 1 || h[0].MaxTempC != 61.5 || !h[0].Throttled {
		t.Fatalf("mismo arranque: %+v", h)
	}

	// Un arranque nuevo se añade a continuación del anterior.
	fakeSystem(t, "boot-2")
	r3 := &Reader{ThermalFile: file}
	r3.loadThermal()
	setTemp(44)
	r3.sample()
	h := r3.Read().Thermal
	if len(h) != 2 || h[0].MaxTempC != 61.5 || h[1].MaxTempC != 44 || h[1].Throttled {
		t.Fatalf("arranque nuevo: %+v", h)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(file), ".thermal.tmp")); err == nil {
		t.Fatal("no debe quedar el temporal de la escritura atómica")
	}
}

func TestThermalWithoutFirmwareReadingReportsNoFlags(t *testing.T) {
	setTemp, setFlags := fakeSystem(t, "boot-x")
	setFlags(0xffffffff, false) // vcgencmd no disponible: no se inventan banderas
	r := &Reader{}
	r.loadThermal()
	setTemp(40)
	r.sample()
	in := r.Read()
	if in.Undervoltage || in.Throttled || in.UVSinceBoot || in.ThrottledSinceBoot {
		t.Fatalf("sin lectura del firmware no hay banderas: %+v", in)
	}
}

func TestThermalHistoryIsCapped(t *testing.T) {
	file := filepath.Join(t.TempDir(), "thermal.json")
	for i := 0; i < thermalKeep+5; i++ {
		setTemp, _ := fakeSystem(t, "boot-"+strconv.Itoa(i))
		r := &Reader{ThermalFile: file}
		r.loadThermal()
		setTemp(float64(40 + i%10))
		r.sample()
	}
	b, _ := os.ReadFile(file)
	var h []ThermalBoot
	if err := json.Unmarshal(b, &h); err != nil || len(h) != thermalKeep {
		t.Fatalf("el historial debe quedar en %d arranques: %d %v", thermalKeep, len(h), err)
	}
	if h[len(h)-1].BootID != "boot-"+strconv.Itoa(thermalKeep+4) {
		t.Fatalf("lo último debe ser el arranque más reciente: %s", h[len(h)-1].BootID)
	}
}
