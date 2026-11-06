package sysinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHeartbeatSurvivesRebootAndIgnoresSameBoot(t *testing.T) {
	dir := t.TempDir()
	boot := filepath.Join(dir, "boot_id")
	hb := filepath.Join(dir, "heartbeat.json")
	oldBoot := bootIDPath
	bootIDPath = boot
	t.Cleanup(func() { bootIDPath = oldBoot })
	write := func(id string) { mustWrite(t, boot, id+"\n") }

	write("arranque-1")
	r1 := &Reader{HeartbeatFile: hb}
	r1.loadHeartbeat()
	if r1.prev != nil {
		t.Fatalf("sin fichero previo no debe haber señal anterior: %+v", r1.prev)
	}
	r1.beat()
	// Mismo arranque (reinicio del daemon): no es un corte de corriente.
	r1b := &Reader{HeartbeatFile: hb}
	r1b.loadHeartbeat()
	if r1b.prev != nil {
		t.Fatalf("el mismo arranque no cuenta como anterior: %+v", r1b.prev)
	}
	// Arranque nuevo: aparece la señal del anterior.
	write("arranque-2")
	r2 := &Reader{HeartbeatFile: hb}
	r2.loadHeartbeat()
	if r2.prev == nil || r2.prev.BootID != "arranque-1" || r2.prev.At.IsZero() {
		t.Fatalf("falta la señal del arranque anterior: %+v", r2.prev)
	}
	var in Info
	r2.thermalView(&in)
	if in.PrevAlive == nil {
		t.Fatal("Info no expone prev_alive")
	}
	// El servicio se reinicia dentro del arranque 2 (un despliegue): no pierde el dato.
	r2.beat()
	r2b := &Reader{HeartbeatFile: hb}
	r2b.loadHeartbeat()
	if r2b.prev == nil || r2b.prev.BootID != "arranque-1" {
		t.Fatalf("el reinicio del servicio perdió la señal del arranque anterior: %+v", r2b.prev)
	}
	r2b.beat()
	r2c := &Reader{HeartbeatFile: hb}
	r2c.loadHeartbeat()
	if r2c.prev == nil || r2c.prev.BootID != "arranque-1" {
		t.Fatalf("un segundo reinicio también debe conservarla: %+v", r2c.prev)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}
