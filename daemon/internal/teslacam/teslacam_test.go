package teslacam

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, root, rel string, size int) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Estructura de un pendrive del coche.
func fakeDrive(t *testing.T) string {
	root := t.TempDir()
	for _, cam := range []string{"front", "back", "left_repeater", "right_repeater"} {
		touch(t, root, "TeslaCam/SavedClips/2025-03-29_14-26-22/2025-03-29_14-15-50-"+cam+".mp4", 1000)
		touch(t, root, "TeslaCam/SavedClips/2025-03-29_14-26-22/2025-03-29_14-16-55-"+cam+".mp4", 1000)
		touch(t, root, "TeslaCam/SentryClips/2025-03-29_12-49-14/2025-03-29_12-46-04-"+cam+".mp4", 1000)
		// dos trayectos en RecentClips separados por más de 3 min
		touch(t, root, "TeslaCam/RecentClips/2025-03-29_14-26-17-"+cam+".mp4", 1000)
		touch(t, root, "TeslaCam/RecentClips/2025-03-29_14-27-21-"+cam+".mp4", 1000)
		touch(t, root, "TeslaCam/RecentClips/2025-03-29_20-00-00-"+cam+".mp4", 1000)
		touch(t, root, "TeslaCam/EncryptedClips/SentryClips/2024-11-20_13-42-35/2024-11-20_13-39-27-"+cam+".mp4", 1000)
	}
	os.WriteFile(filepath.Join(root, "TeslaCam/SavedClips/2025-03-29_14-26-22/event.json"),
		[]byte(`{"timestamp":"2025-03-29T14:26:15","city":"Springfield","street":"Evergreen Terrace","est_lat":"0.0001","est_lon":"0.0002","reason":"user_interaction_dashcam_launcher_action_tapped","camera":"0"}`), 0o644)
	touch(t, root, "TeslaCam/SavedClips/2025-03-29_14-26-22/thumb.png", 10)
	touch(t, root, "TeslaCam/EncryptedClips/-README_es.txt", 10)
	touch(t, root, "TeslaCam/.Spotlight-V100/basura", 10)
	return root
}

func TestWalkClassifies(t *testing.T) {
	root := fakeDrive(t)
	files, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	enc := 0
	for _, f := range files {
		counts[f.Category]++
		if f.Encrypted {
			enc++
		}
		if filepath.Base(filepath.Dir(f.Rel)) == ".Spotlight-V100" {
			t.Fatal("no debe recorrer carpetas ocultas")
		}
	}
	if counts[Saved] != 10 || counts[Sentry] != 8 || counts[Recent] != 12 || counts[Other] != 1 || enc != 5 {
		t.Fatalf("conteos inesperados %v enc=%d", counts, enc)
	}
}

func TestWalkWithoutTeslaCam(t *testing.T) {
	// Recién formateado por el coche puede no existir nada: no es un error.
	files, err := Walk(t.TempDir())
	if err != nil || len(files) != 0 {
		t.Fatalf("files=%v err=%v", files, err)
	}
}

func TestEvents(t *testing.T) {
	root := fakeDrive(t)
	files, _ := Walk(root)
	evs := Events(root, files)
	var saved, recent, encSentry int
	for _, e := range evs {
		switch {
		case e.Category == Saved:
			saved++
			if e.Meta == nil || e.Meta.City != "Springfield" || e.Thumb == "" {
				t.Fatalf("meta/thumb de SavedClips: %+v", e)
			}
			if len(e.Segments) != 2 || len(e.Cameras) != 4 || e.End != "2025-03-29_14-17-55" {
				t.Fatalf("segmentos %+v", e)
			}
		case e.Category == Recent:
			recent++
		case e.Category == Sentry && e.Encrypted:
			encSentry++
			if e.Meta != nil {
				t.Fatal("event.json cifrado no debe leerse")
			}
		}
	}
	if saved != 1 || recent != 2 || encSentry != 1 {
		t.Fatalf("saved=%d recent=%d encSentry=%d", saved, recent, encSentry)
	}
	if evs[0].Start != "2025-03-29_20-00-00" {
		t.Fatalf("orden: primero el más reciente, got %s", evs[0].Start)
	}
}

// Con los clips reales copiados del coche (.devdata), si existen.
func TestRealDevData(t *testing.T) {
	root := filepath.Join("..", "..", "..", ".devdata")
	if _, err := os.Stat(filepath.Join(root, "TeslaCam")); err != nil {
		t.Skip("sin .devdata")
	}
	files, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	evs := Events(root, files)
	if len(evs) == 0 {
		t.Fatal("sin eventos")
	}
	for _, e := range evs {
		if len(e.Segments) == 0 {
			continue
		}
		first := e.Segments[0].Cameras["front"]
		plain := IsPlainMP4(filepath.Join(root, filepath.FromSlash(first)))
		if plain == e.Encrypted {
			t.Fatalf("%s: encrypted=%v pero IsPlainMP4=%v", e.Dir, e.Encrypted, plain)
		}
	}
}
