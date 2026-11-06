package archive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"teslapi/internal/teslacam"
)

const vol = "exfat-ABCD-1048576"

func openIx(t *testing.T) *Index {
	ix, err := OpenIndex(filepath.Join(t.TempDir(), "ix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix
}

func write(t *testing.T, root, rel string, size int, mtime time.Time) teslacam.File {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mtime, mtime)
	files, _ := teslacam.Walk(root)
	for _, f := range files {
		if f.Rel == rel {
			return f
		}
	}
	t.Fatalf("no encontrado %s", rel)
	return teslacam.File{}
}

var t0 = time.Date(2025, 3, 29, 12, 0, 0, 0, time.UTC)

func rels(fs []teslacam.File) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rel)
	}
	return out
}

func TestPlanPriorityAndOrder(t *testing.T) {
	root := t.TempDir()
	write(t, root, "TeslaCam/RecentClips/2025-03-29_11-00-00-front.mp4", 200_000, t0)
	write(t, root, "TeslaCam/SentryClips/2025-03-29_10-00-00/2025-03-29_09-59-00-front.mp4", 200_000, t0)
	write(t, root, "TeslaCam/SavedClips/2025-03-29_11-30-00/2025-03-29_11-29-00-front.mp4", 200_000, t0)
	write(t, root, "TeslaCam/SavedClips/2025-03-28_08-00-00/2025-03-28_07-59-00-front.mp4", 200_000, t0)
	write(t, root, "TeslaCam/SavedClips/2025-03-28_08-00-00/event.json", 200, t0)
	write(t, root, "TeslaCam/RecentClips/2025-03-29_11-01-00-front.mp4", 5_000, t0) // resto de clip cortado
	files, _ := teslacam.Walk(root)
	got := rels(Plan(files, openIx(t), vol, PlanOptions{RecentClips: true, MinClipBytes: 100_000, CarIdle: true}))
	want := []string{
		"TeslaCam/SavedClips/2025-03-28_08-00-00/2025-03-28_07-59-00-front.mp4",
		"TeslaCam/SavedClips/2025-03-28_08-00-00/event.json", // event.json es pequeño pero no es clip: se copia
		"TeslaCam/SavedClips/2025-03-29_11-30-00/2025-03-29_11-29-00-front.mp4",
		"TeslaCam/SentryClips/2025-03-29_10-00-00/2025-03-29_09-59-00-front.mp4",
		"TeslaCam/RecentClips/2025-03-29_11-00-00-front.mp4",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPlanSkipsFilesBeingWrittenOnlyWhenCarActive(t *testing.T) {
	root := t.TempDir()
	write(t, root, "TeslaCam/RecentClips/2025-03-29_11-00-00-front.mp4", 200_000, t0)
	write(t, root, "TeslaCam/RecentClips/2025-03-29_11-01-00-front.mp4", 200_000, t0.Add(60*time.Second))
	files, _ := teslacam.Walk(root)
	ix := openIx(t)
	active := Plan(files, ix, vol, PlanOptions{RecentClips: true, Settle: 30 * time.Second})
	if len(active) != 1 || !strings.Contains(active[0].Rel, "11-00-00") {
		t.Fatalf("con el coche grabando, el último clip no debe copiarse: %v", rels(active))
	}
	idle := Plan(files, ix, vol, PlanOptions{RecentClips: true, Settle: 30 * time.Second, CarIdle: true})
	if len(idle) != 2 {
		t.Fatalf("con el coche quieto se copia todo (el último minuto del trayecto incluido): %v", rels(idle))
	}
	none := Plan(files, ix, vol, PlanOptions{RecentClips: false, CarIdle: true})
	if len(none) != 0 {
		t.Fatalf("RecentClips desactivado: %v", rels(none))
	}
}

func TestDestRel(t *testing.T) {
	cases := map[string]string{
		"TeslaCam/RecentClips/2025-03-29_12-26-17-front.mp4":               "RecentClips/2025-03-29/2025-03-29_12-26-17-front.mp4",
		"TeslaCam/EncryptedClips/RecentClips/2024-11-20_11-39-27-back.mp4": "EncryptedClips/RecentClips/2024-11-20/2024-11-20_11-39-27-back.mp4",
		"TeslaCam/SavedClips/2025-03-29_12-26-22/event.json":               "SavedClips/2025-03-29_12-26-22/event.json",
		"TeslaTrackMode/lapvideo.mp4":                                      "TeslaTrackMode/lapvideo.mp4",
		"TeslaCam/RecentClips/thumb.png":                                   "RecentClips/thumb.png",
	}
	for in, want := range cases {
		if got := DestRel(in); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}

func TestCopyAndResume(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	ix := openIx(t)
	write(t, src, "TeslaCam/SavedClips/e1/2025-03-29_11-29-00-front.mp4", 300_000, t0)
	write(t, src, "TeslaCam/SavedClips/e1/2025-03-29_11-29-00-back.mp4", 300_000, t0)
	files, _ := teslacam.Walk(src)
	items := Plan(files, ix, vol, PlanOptions{MinClipBytes: 1, CarIdle: true})

	// Cancelado antes de empezar: nada copiado ni anotado, sin .part
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Copy(ctx, src, dst, items, ix, vol, &Progress{}); err == nil {
		t.Fatal("debía devolver el error de contexto")
	}
	if n, _ := CleanParts(dst); n != 0 {
		t.Fatalf("quedaron %d .part", n)
	}
	if ix.Has(vol, items[0].Rel, items[0].Size) {
		t.Fatal("no debe anotarse lo no copiado")
	}

	res, err := Copy(context.Background(), src, dst, items, ix, vol, &Progress{})
	if err != nil || res.Files != 2 || res.Bytes != 600_000 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	for _, f := range items {
		a, _ := os.ReadFile(filepath.Join(src, f.Rel))
		b, err := os.ReadFile(filepath.Join(dst, DestRel(f.Rel)))
		if err != nil || string(a) != string(b) {
			t.Fatalf("contenido distinto en %s", f.Rel)
		}
	}
	if left := Plan(files, ix, vol, PlanOptions{MinClipBytes: 1, CarIdle: true}); len(left) != 0 {
		t.Fatalf("tras copiar no debe quedar nada pendiente: %v", rels(left))
	}
	// Un fichero que el coche ha reescrito con otro tamaño vuelve a estar pendiente.
	write(t, src, "TeslaCam/SavedClips/e1/2025-03-29_11-29-00-back.mp4", 310_000, t0)
	files, _ = teslacam.Walk(src)
	if left := Plan(files, ix, vol, PlanOptions{MinClipBytes: 1, CarIdle: true}); len(left) != 1 {
		t.Fatalf("debía detectar el cambio de tamaño: %v", rels(left))
	}
	// Otro formateo (otro volumen) no hereda lo archivado.
	if left := Plan(files, ix, "exfat-OTRO-1048576", PlanOptions{MinClipBytes: 1, CarIdle: true}); len(left) != 2 {
		t.Fatalf("un volumen nuevo empieza de cero: %v", rels(left))
	}
}

func TestCopyRecognisesAlreadyOnNAS(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	ix := openIx(t)
	f := write(t, src, "TeslaCam/SavedClips/e1/2025-03-29_11-29-00-front.mp4", 300_000, t0)
	p := filepath.Join(dst, DestRel(f.Rel)) // índice perdido, pero ya está en el NAS
	os.MkdirAll(filepath.Dir(p), 0o755)
	body, _ := os.ReadFile(filepath.Join(src, f.Rel))
	os.WriteFile(p, body, 0o644)
	res, err := Copy(context.Background(), src, dst, []teslacam.File{f}, ix, vol, &Progress{})
	if err != nil || res.Files != 0 || !ix.Has(vol, f.Rel, f.Size) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestDeletableOnlyFullyArchivedEventsNeverRecent(t *testing.T) {
	src := t.TempDir()
	ix := openIx(t)
	a1 := write(t, src, "TeslaCam/SentryClips/2025-03-01_10-00-00/a-front.mp4", 1000, t0)
	a2 := write(t, src, "TeslaCam/SentryClips/2025-03-01_10-00-00/event.json", 10, t0)
	b1 := write(t, src, "TeslaCam/SentryClips/2025-03-02_10-00-00/b-front.mp4", 1000, t0)
	write(t, src, "TeslaCam/SentryClips/2025-03-02_10-00-00/b-back.mp4", 1000, t0) // sin archivar
	r := write(t, src, "TeslaCam/RecentClips/2025-03-03_10-00-00-front.mp4", 1000, t0)
	for _, f := range []teslacam.File{a1, a2, b1, r} {
		ix.Mark(vol, f.Rel, f.Size)
	}
	files, _ := teslacam.Walk(src)
	got, freed := Deletable(files, ix, vol, 1<<40)
	if strings.Join(got, ",") != a1.Rel+","+a2.Rel || freed != 1010 {
		t.Fatalf("got %v freed %d", got, freed)
	}
	if got, _ := Deletable(files, ix, vol, 0); len(got) != 0 {
		t.Fatalf("sin necesidad de espacio no se borra nada: %v", got)
	}
}

func TestHistory(t *testing.T) {
	ix := openIx(t)
	for i := 0; i < 3; i++ {
		ix.AddRun(Run{Start: t0.Add(time.Duration(i) * time.Hour), Files: i, Result: "ok"})
	}
	h := ix.History(2)
	if len(h) != 2 || h[0].Files != 2 {
		t.Fatalf("history %+v", h)
	}
}

// Un fichero del mismo tamaño en el NAS no es una copia: se sobrescribe, y lo que se anota
// como archivado es lo que de verdad se copió.
func TestCopyOverwritesSameSizeWithDifferentContent(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	rel := "TeslaCam/SentryClips/ev/a.mp4"
	os.MkdirAll(filepath.Join(src, "TeslaCam/SentryClips/ev"), 0o755)
	os.WriteFile(filepath.Join(src, rel), []byte("VIDEO-ORIGINAL"), 0o644)
	os.MkdirAll(filepath.Join(dst, "SentryClips/ev"), 0o755)
	os.WriteFile(filepath.Join(dst, "SentryClips/ev/a.mp4"), []byte("XXXXXX-FALSO-X"), 0o644)
	ix := openIx(t)
	f := teslacam.File{Rel: rel, Size: 14, Category: teslacam.Sentry}
	res, err := Copy(context.Background(), src, dst, []teslacam.File{f}, ix, vol, &Progress{})
	if err != nil || res.Files != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "SentryClips/ev/a.mp4")); string(got) != "VIDEO-ORIGINAL" {
		t.Fatalf("NAS: %q", got)
	}
	// Con el mismo contenido no se vuelve a copiar.
	res, err = Copy(context.Background(), src, dst, []teslacam.File{f}, ix, vol, &Progress{})
	if err != nil || res.Files != 0 {
		t.Fatalf("idéntico: %v %+v", err, res)
	}
}

// Antes de borrar del pendrive se compara con el NAS: lo manipulado o dañado se conserva y
// se vuelve a copiar, y la carpeta entera queda a salvo.
func TestVerifyKeepsOnlyIdenticalFolders(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	put := func(root, rel, body string) {
		os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755)
		os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644)
	}
	good, bad1, bad2 := "TeslaCam/SavedClips/ok/a.mp4", "TeslaCam/SavedClips/mal/a.mp4", "TeslaCam/SavedClips/mal/b.mp4"
	put(src, good, "AAAA")
	put(src, bad1, "BBBB")
	put(src, bad2, "CCCC")
	put(dst, "SavedClips/ok/a.mp4", "AAAA")
	put(dst, "SavedClips/mal/a.mp4", "BBBB")
	put(dst, "SavedClips/mal/b.mp4", "XXXX")
	ix := openIx(t)
	var files []teslacam.File
	for _, r := range []string{good, bad1, bad2} {
		files = append(files, teslacam.File{Rel: r, Size: 4, Category: teslacam.Saved})
		ix.Mark(vol, r, 4)
	}
	keep, freed, err := Verify(context.Background(), src, dst, files, []string{good, bad1, bad2}, ix, vol)
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != 1 || keep[0] != good || freed != 4 {
		t.Fatalf("keep=%v freed=%d", keep, freed)
	}
	if ix.Has(vol, bad2, 4) || !ix.Has(vol, bad1, 4) {
		t.Fatal("solo se olvida lo que no coincide, para volver a copiarlo")
	}
}

// Un enlace simbólico en el NAS no saca la escritura de la carpeta de destino.
func TestCopyDoesNotFollowSymlinkOutOfDestination(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	rel := "TeslaCam/SavedClips/ev/a.mp4"
	os.MkdirAll(filepath.Join(src, "TeslaCam/SavedClips/ev"), 0o755)
	os.WriteFile(filepath.Join(src, rel), []byte("VIDEO"), 0o644)
	if err := os.Symlink(outside, filepath.Join(dst, "SavedClips")); err != nil {
		t.Skip(err)
	}
	_, err := Copy(context.Background(), src, dst, []teslacam.File{{Rel: rel, Size: 5, Category: teslacam.Saved}}, openIx(t), vol, &Progress{})
	if err == nil {
		t.Fatal("debía fallar")
	}
	if n, _ := os.ReadDir(outside); len(n) != 0 {
		t.Fatalf("escribió fuera: %v", n)
	}
}
