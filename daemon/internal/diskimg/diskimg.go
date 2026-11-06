// Package diskimg localiza el sistema de ficheros dentro de la imagen que ve el coche.
//
// El coche puede formatear el "pendrive" como quiera (exFAT o FAT32, con MBR, con GPT
// o sin tabla de particiones), así que nunca suponemos una estructura fija: se lee
// la imagen cada vez y se devuelve el volumen de datos con su desplazamiento.
package diskimg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const sector = 512

type Volume struct {
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	FSType string `json:"fs_type"` // "exfat", "vfat"
	Serial uint32 `json:"serial"`
	Label  string `json:"label"`
	Scheme string `json:"scheme"` // "mbr", "gpt", "none"
}

// ID identifica un formateo concreto: cambia cuando el coche vuelve a formatear.
func (v Volume) ID() string {
	return fmt.Sprintf("%s-%08X-%d", v.FSType, v.Serial, v.Offset)
}

var ErrNoFilesystem = errors.New("diskimg: no exFAT/FAT filesystem found")

// Detect devuelve el primer volumen exFAT/FAT de la imagen.
func Detect(r io.ReaderAt, size int64) (Volume, error) {
	v, err := detect(r, size)
	return v.Clamp(size), err
}

func detect(r io.ReaderAt, size int64) (Volume, error) {
	s0 := make([]byte, sector)
	if _, err := r.ReadAt(s0, 0); err != nil {
		return Volume{}, fmt.Errorf("diskimg: read sector 0: %w", err)
	}
	// Un sector de arranque de FS también termina en 55AA, así que se mira antes
	// si el sector 0 ya es un sistema de ficheros ("superfloppy").
	if v, ok := parseBootSector(s0); ok {
		v.Offset, v.Scheme = 0, "none"
		if v.Size == 0 || v.Size > size {
			v.Size = size
		}
		return v, nil
	}
	if s0[510] != 0x55 || s0[511] != 0xAA {
		return Volume{}, ErrNoFilesystem
	}
	for i := 0; i < 4; i++ {
		e := s0[446+16*i : 446+16*(i+1)]
		ptype := e[4]
		if ptype == 0xEE {
			return detectGPT(r, size)
		}
	}
	for i := 0; i < 4; i++ {
		e := s0[446+16*i : 446+16*(i+1)]
		if e[4] == 0 {
			continue
		}
		start := int64(binary.LittleEndian.Uint32(e[8:])) * sector
		count := int64(binary.LittleEndian.Uint32(e[12:])) * sector
		if v, ok := probe(r, start, count); ok {
			v.Scheme = "mbr"
			return v, nil
		}
	}
	return Volume{}, ErrNoFilesystem
}

func detectGPT(r io.ReaderAt, size int64) (Volume, error) {
	h := make([]byte, sector)
	if _, err := r.ReadAt(h, sector); err != nil {
		return Volume{}, fmt.Errorf("diskimg: read GPT header: %w", err)
	}
	if !bytes.Equal(h[:8], []byte("EFI PART")) {
		return Volume{}, ErrNoFilesystem
	}
	entriesLBA := int64(binary.LittleEndian.Uint64(h[72:]))
	n := int(binary.LittleEndian.Uint32(h[80:]))
	esz := int(binary.LittleEndian.Uint32(h[84:]))
	// La imagen la escribe quien esté al otro lado del USB: sin tope, una cabecera
	// manipulada pide terabytes de memoria.
	if esz < 128 || esz > 4096 || n <= 0 || n > 1024 || entriesLBA < 0 || entriesLBA > size/sector {
		return Volume{}, ErrNoFilesystem
	}
	buf := make([]byte, n*esz)
	if _, err := r.ReadAt(buf, entriesLBA*sector); err != nil {
		return Volume{}, fmt.Errorf("diskimg: read GPT entries: %w", err)
	}
	zero := make([]byte, 16)
	for i := 0; i < n; i++ {
		e := buf[i*esz : (i+1)*esz]
		if bytes.Equal(e[:16], zero) {
			continue
		}
		first := int64(binary.LittleEndian.Uint64(e[32:]))
		last := int64(binary.LittleEndian.Uint64(e[40:]))
		if first < 0 || last < first || first > size/sector || last > size/sector {
			continue
		}
		start, count := first*sector, (last-first+1)*sector
		if start+count > size {
			count = size - start
		}
		if v, ok := probe(r, start, count); ok {
			v.Scheme = "gpt"
			return v, nil
		}
	}
	return Volume{}, ErrNoFilesystem
}

func probe(r io.ReaderAt, off, size int64) (Volume, bool) {
	b := make([]byte, sector)
	if _, err := r.ReadAt(b, off); err != nil {
		return Volume{}, false
	}
	v, ok := parseBootSector(b)
	if !ok {
		return Volume{}, false
	}
	v.Offset = off
	if v.Size == 0 || v.Size > size {
		v.Size = size
	}
	return v, true
}

// Clamp evita que el volumen se salga de la imagen (el disco real del coche declara
// en el MBR una partición que llega más allá del último sector).
func (v Volume) Clamp(imageSize int64) Volume {
	if v.Offset+v.Size > imageSize {
		v.Size = imageSize - v.Offset
	}
	return v
}

func parseBootSector(b []byte) (Volume, bool) {
	if b[510] != 0x55 || b[511] != 0xAA {
		return Volume{}, false
	}
	if bytes.Equal(b[3:11], []byte("EXFAT   ")) {
		shift := b[108] // BytesPerSectorShift: de 512 B a 4 KiB según la especificación
		vlen := binary.LittleEndian.Uint64(b[72:])
		if shift < 9 || shift > 12 || vlen > 1<<52 {
			return Volume{}, false
		}
		return Volume{
			FSType: "exfat",
			Size:   int64(vlen) << shift,
			Serial: binary.LittleEndian.Uint32(b[100:]),
		}, true
	}
	bps := int64(binary.LittleEndian.Uint16(b[11:]))
	total := int64(binary.LittleEndian.Uint16(b[19:]))
	if total == 0 {
		total = int64(binary.LittleEndian.Uint32(b[32:]))
	}
	switch {
	case bytes.Equal(b[82:87], []byte("FAT32")):
		return Volume{FSType: "vfat", Size: total * bps, Serial: binary.LittleEndian.Uint32(b[67:]), Label: trim(b[71:82])}, true
	case bytes.Equal(b[54:59], []byte("FAT16")), bytes.Equal(b[54:59], []byte("FAT12")):
		return Volume{FSType: "vfat", Size: total * bps, Serial: binary.LittleEndian.Uint32(b[39:]), Label: trim(b[43:54])}, true
	}
	return Volume{}, false
}

func trim(b []byte) string {
	return string(bytes.TrimRight(b, " \x00"))
}
