package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"teslapi/internal/teslacam"
)

// Copia y comprueba sobre un NAS de verdad: TESLAPI_NAS_TEST=<carpeta dentro del montaje>.
// Trabaja en una carpeta temporal y la borra al terminar.
func TestCopyAndVerifyOnRealNAS(t *testing.T) {
	root := os.Getenv("TESLAPI_NAS_TEST")
	if root == "" {
		t.Skip("solo en la Pi: TESLAPI_NAS_TEST=<carpeta del NAS montado>")
	}
	dst, err := os.MkdirTemp(root, ".teslapi-prueba-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dst)
	src := t.TempDir()
	rel := "TeslaCam/SavedClips/prueba/2026-01-01_10-00-00-front.mp4"
	body := make([]byte, 2<<20)
	for i := range body {
		body[i] = byte(i % 251)
	}
	os.MkdirAll(filepath.Join(src, filepath.Dir(rel)), 0o755)
	os.WriteFile(filepath.Join(src, rel), body, 0o644)
	ix, err := OpenIndex(filepath.Join(t.TempDir(), "ix.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	f := teslacam.File{Rel: rel, Size: int64(len(body)), Category: teslacam.Saved}
	res, err := Copy(context.Background(), src, dst, []teslacam.File{f}, ix, "v", &Progress{})
	if err != nil || res.Files != 1 {
		t.Fatalf("copiar: %v %+v", err, res)
	}
	if got, err := os.ReadFile(filepath.Join(dst, DestRel(rel))); err != nil || len(got) != len(body) {
		t.Fatalf("leer del NAS: %v %d", err, len(got))
	}
	if res, err = Copy(context.Background(), src, dst, []teslacam.File{f}, ix, "v", &Progress{}); err != nil || res.Files != 0 {
		t.Fatalf("idéntico no se recopia: %v %+v", err, res)
	}
	keep, freed, err := Verify(context.Background(), src, dst, []teslacam.File{f}, []string{rel}, ix, "v")
	if err != nil || len(keep) != 1 || freed != f.Size {
		t.Fatalf("verificar: %v %v %d", err, keep, freed)
	}
	// Al dañar la copia en el NAS, ya no se puede borrar del pendrive.
	os.WriteFile(filepath.Join(dst, DestRel(rel)), make([]byte, len(body)), 0o644)
	keep, _, err = Verify(context.Background(), src, dst, []teslacam.File{f}, []string{rel}, ix, "v")
	if err != nil || len(keep) != 0 {
		t.Fatalf("copia dañada: %v %v", err, keep)
	}
}
