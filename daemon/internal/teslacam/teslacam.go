// Package teslacam interpreta la estructura que escribe el coche en el pendrive:
//
//	TeslaCam/{RecentClips,SavedClips,SentryClips}/...
//	TeslaCam/EncryptedClips/{RecentClips,SavedClips,SentryClips}/...  (grabaciones cifradas)
//	TeslaTrackMode/...
//
// Los clips son "YYYY-MM-DD_HH-MM-SS-<cámara>.mp4", de ~1 min, uno por cámara.
// Las horas son las del reloj del coche, sin zona: se tratan como texto ordenable.
package teslacam

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	Saved  = "SavedClips"
	Sentry = "SentryClips"
	Recent = "RecentClips"
	Track  = "TeslaTrackMode"
	// Other: ficheros sueltos (README del coche, etc.). Se archivan pero no son eventos.
	Other = "Other"
)

// Categories en orden de prioridad de archivado: lo que el usuario guardó a
// propósito primero, luego Centinela; RecentClips, que es lo más voluminoso, al final.
var Categories = []string{Saved, Sentry, Track, Recent}

var clipRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2})-([a-z_]+)\.mp4$`)

const stampLayout = "2006-01-02_15-04-05"

// File es un fichero del pendrive con la información que necesita el archivador.
type File struct {
	Rel       string    `json:"rel"` // relativa a la raíz del volumen, con "/"
	Size      int64     `json:"size"`
	MTime     time.Time `json:"mtime"`
	Category  string    `json:"category"`
	Encrypted bool      `json:"encrypted"`
	IsClip    bool      `json:"is_clip"`
}

// Walk lista todos los ficheros de TeslaCam y TeslaTrackMode bajo root.
func Walk(root string) ([]File, error) {
	a, err := WalkUnder(filepath.Join(root, "TeslaCam"), "TeslaCam/")
	if err != nil {
		return nil, err
	}
	b, err := WalkUnder(filepath.Join(root, Track), Track+"/")
	if err != nil {
		return nil, err
	}
	return append(a, b...), nil
}

// WalkUnder recorre dir y da a cada fichero la ruta prefix+relativa. Sirve también
// para el NAS, donde la carpeta de destino hace las veces de TeslaCam/.
func WalkUnder(dir, prefix string) ([]File, error) {
	var out []File
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == dir {
				return fs.SkipDir
			}
			return err
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") && p != dir {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || strings.HasSuffix(name, ".part") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = prefix + filepath.ToSlash(rel)
		cat, enc := classify(rel)
		out = append(out, File{
			Rel: rel, Size: info.Size(), MTime: info.ModTime(),
			Category: cat, Encrypted: enc, IsClip: strings.HasSuffix(name, ".mp4"),
		})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return out, nil
}

func classify(rel string) (category string, encrypted bool) {
	parts := strings.Split(rel, "/")
	if parts[0] == Track || (len(parts) > 2 && parts[1] == Track) {
		return Track, false
	}
	if len(parts) > 1 && parts[1] == "EncryptedClips" {
		if len(parts) > 3 {
			return parts[2], true
		}
		return Other, true
	}
	if len(parts) > 2 {
		return parts[1], false
	}
	return Other, false
}

// Meta es event.json tal como lo escribe el coche (todo en texto).
type Meta struct {
	Timestamp string `json:"timestamp"`
	City      string `json:"city"`
	Street    string `json:"street"`
	Lat       string `json:"est_lat"`
	Lon       string `json:"est_lon"`
	Reason    string `json:"reason"`
	Camera    string `json:"camera"`
}

// Segment es un minuto de grabación: un fichero por cámara.
type Segment struct {
	Start   string            `json:"start"`
	Cameras map[string]string `json:"cameras"` // cámara → ruta relativa
}

type Event struct {
	ID        string    `json:"id"`
	Category  string    `json:"category"`
	Encrypted bool      `json:"encrypted"`
	Dir       string    `json:"dir"`
	Start     string    `json:"start"`
	End       string    `json:"end"`
	Thumb     string    `json:"thumb,omitempty"`
	Meta      *Meta     `json:"meta,omitempty"`
	Bytes     int64     `json:"bytes"`
	Cameras   []string  `json:"cameras"`
	Segments  []Segment `json:"segments,omitempty"`
}

// recentGap: en RecentClips, un hueco mayor que esto entre minutos empieza otro trayecto.
const recentGap = 3 * time.Minute

// Events agrupa los ficheros en eventos (carpeta para Saved/Sentry, trayecto para Recent).
func Events(root string, files []File) []Event {
	byDir := map[string][]File{}
	for _, f := range files {
		byDir[path.Dir(f.Rel)] = append(byDir[path.Dir(f.Rel)], f)
	}
	var out []Event
	for dir, fs := range byDir {
		cat, enc := classify(dir + "/x")
		if cat == Recent {
			out = append(out, splitRecent(dir, cat, enc, fs)...)
			continue
		}
		ev := buildEvent(dir, cat, enc, fs)
		if ev.Start == "" && ev.Thumb == "" {
			continue // carpetas sin clips (p.ej. solo README)
		}
		ev.ID = path.Base(dir)
		if !enc {
			ev.Meta = ReadMeta(filepath.Join(root, filepath.FromSlash(dir), "event.json"))
		}
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start > out[j].Start })
	return out
}

func buildEvent(dir, cat string, enc bool, files []File) Event {
	ev := Event{Category: cat, Encrypted: enc, Dir: dir}
	segs := map[string]*Segment{}
	cams := map[string]bool{}
	for _, f := range files {
		ev.Bytes += f.Size
		name := path.Base(f.Rel)
		if name == "thumb.png" {
			ev.Thumb = f.Rel
			continue
		}
		m := clipRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		s := segs[m[1]]
		if s == nil {
			s = &Segment{Start: m[1], Cameras: map[string]string{}}
			segs[m[1]] = s
		}
		s.Cameras[m[2]] = f.Rel
		cams[m[2]] = true
	}
	for _, s := range segs {
		ev.Segments = append(ev.Segments, *s)
	}
	sort.Slice(ev.Segments, func(i, j int) bool { return ev.Segments[i].Start < ev.Segments[j].Start })
	if n := len(ev.Segments); n > 0 {
		ev.Start = ev.Segments[0].Start
		if t, err := time.Parse(stampLayout, ev.Segments[n-1].Start); err == nil {
			ev.End = t.Add(time.Minute).Format(stampLayout)
		}
	}
	for c := range cams {
		ev.Cameras = append(ev.Cameras, c)
	}
	sort.Strings(ev.Cameras)
	return ev
}

func splitRecent(dir, cat string, enc bool, files []File) []Event {
	all := buildEvent(dir, cat, enc, files)
	var out []Event
	var cur []Segment
	flush := func() {
		if len(cur) == 0 {
			return
		}
		var fs []File
		for _, s := range cur {
			for _, rel := range s.Cameras {
				fs = append(fs, File{Rel: rel, Size: sizeOf(files, rel)})
			}
		}
		ev := buildEvent(dir, cat, enc, fs)
		ev.ID = ev.Start
		out = append(out, ev)
		cur = nil
	}
	var prev time.Time
	for _, s := range all.Segments {
		t, err := time.Parse(stampLayout, s.Start)
		if err != nil {
			continue
		}
		if len(cur) > 0 && t.Sub(prev) > recentGap {
			flush()
		}
		cur = append(cur, s)
		prev = t
	}
	flush()
	return out
}

func sizeOf(files []File, rel string) int64 {
	for _, f := range files {
		if f.Rel == rel {
			return f.Size
		}
	}
	return 0
}

// metaMax: un event.json real ocupa unos cientos de bytes; la imagen del pendrive o el NAS
// pueden traer cualquier cosa, y no se debe cargar entera en memoria.
const metaMax = 64 << 10

// ReadMeta lee un event.json; nil si no existe o no se puede interpretar (cifrado).
func ReadMeta(p string) *Meta {
	rt, err := os.OpenRoot(filepath.Dir(p))
	if err != nil {
		return nil
	}
	defer rt.Close()
	f, err := rt.Open(filepath.Base(p))
	if err != nil {
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, metaMax))
	if err != nil {
		return nil
	}
	var m Meta
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return &m
}

// IsPlainMP4 distingue un clip normal de uno cifrado: el MP4 lleva "ftyp" en el byte 4.
func IsPlainMP4(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 8)
	if _, err := f.ReadAt(b, 0); err != nil {
		return false
	}
	return string(b[4:8]) == "ftyp"
}
