package diskimg

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T, name string) *bytes.Reader {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "..", "test", "fixtures", name+".img.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(b)
}

// Imágenes reales generadas con hdiutil (macOS): mismas variantes que puede dejar el coche.
func TestDetectFixtures(t *testing.T) {
	cases := []struct {
		name, fs, scheme string
		offsetNonZero    bool
	}{
		{"exfat-mbr", "exfat", "mbr", true},
		{"exfat-gpt", "exfat", "gpt", true},
		{"exfat-none", "exfat", "none", false},
		{"fat32-mbr", "vfat", "mbr", true},
		{"fat32-none", "vfat", "none", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := loadFixture(t, c.name)
			v, err := Detect(r, r.Size())
			if err != nil {
				t.Fatal(err)
			}
			if v.FSType != c.fs || v.Scheme != c.scheme {
				t.Fatalf("got fs=%s scheme=%s, want %s/%s", v.FSType, v.Scheme, c.fs, c.scheme)
			}
			if (v.Offset != 0) != c.offsetNonZero {
				t.Fatalf("offset inesperado %d", v.Offset)
			}
			if v.Size <= 0 || v.Offset+v.Size > r.Size() {
				t.Fatalf("tamaño fuera de la imagen: off=%d size=%d img=%d", v.Offset, v.Size, r.Size())
			}
			if v.Serial == 0 {
				t.Fatal("serial vacío: no serviría para detectar un reformateo")
			}
			if v.FSType == "vfat" && v.Label != "TESLACAM" {
				t.Fatalf("label %q", v.Label)
			}
		})
	}
}

// Caso negativo: una imagen recién creada (ceros) o basura no es un volumen.
func TestDetectRejectsBlankAndGarbage(t *testing.T) {
	blank := bytes.NewReader(make([]byte, 1<<20))
	if _, err := Detect(blank, blank.Size()); err != ErrNoFilesystem {
		t.Fatalf("imagen en blanco: err=%v", err)
	}
	g := make([]byte, 1<<20)
	g[510], g[511] = 0x55, 0xAA // firma MBR sin particiones válidas
	gr := bytes.NewReader(g)
	if _, err := Detect(gr, gr.Size()); err != ErrNoFilesystem {
		t.Fatalf("MBR vacío: err=%v", err)
	}
}

// Un reformateo cambia el ID aunque el tipo de FS sea el mismo.
func TestIDChangesWithSerial(t *testing.T) {
	a := Volume{FSType: "exfat", Serial: 1, Offset: 1048576}
	b := a
	b.Serial = 2
	if a.ID() == b.ID() {
		t.Fatal("el ID debe cambiar con el serial")
	}
}

// Una cabecera GPT manipulada no debe provocar una reserva enorme.
func TestDetectRejectsHugeGPTEntries(t *testing.T) {
	b := make([]byte, 4096)
	b[510], b[511], b[446+4] = 0x55, 0xAA, 0xEE
	copy(b[512:], "EFI PART")
	binary.LittleEndian.PutUint64(b[512+72:], 2)
	binary.LittleEndian.PutUint32(b[512+80:], 1024)
	binary.LittleEndian.PutUint32(b[512+84:], 0x7fffffff)
	if _, err := Detect(bytes.NewReader(b), int64(len(b))); !errors.Is(err, ErrNoFilesystem) {
		t.Fatalf("err = %v", err)
	}
}

// El detector lee lo que escribe el otro lado del USB: nunca debe entrar en pánico.
func FuzzDetect(f *testing.F) {
	f.Add(make([]byte, 1024))
	gpt := make([]byte, 1024)
	gpt[510], gpt[511], gpt[446+4] = 0x55, 0xAA, 0xEE
	copy(gpt[512:], "EFI PART")
	f.Add(gpt)
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) < 512 {
			return
		}
		Detect(bytes.NewReader(b), int64(len(b)))
	})
}
