// Package archive decide qué copiar al NAS, lo copia de forma reanudable y
// elige qué borrar del pendrive cuando falta espacio.
package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"teslapi/internal/teslacam"
)

type PlanOptions struct {
	RecentClips  bool
	MinClipBytes int64
	// CarIdle: el coche no ha escrito en la imagen recientemente, así que nada
	// del snapshot está a medio escribir y se puede copiar todo.
	CarIdle bool
	Settle  time.Duration
}

var priority = map[string]int{}

func init() {
	for i, c := range teslacam.Categories {
		priority[c] = i
	}
	priority[teslacam.Other] = len(teslacam.Categories)
}

// Plan devuelve lo pendiente de archivar, en orden de prioridad y de más antiguo a más nuevo.
func Plan(files []teslacam.File, ix *Index, vol string, o PlanOptions) []teslacam.File {
	var newest time.Time
	for _, f := range files {
		if f.MTime.After(newest) {
			newest = f.MTime
		}
	}
	cutoff := newest.Add(-o.Settle)
	var out []teslacam.File
	for _, f := range files {
		if f.Category == teslacam.Recent && !o.RecentClips {
			continue
		}
		if f.IsClip && f.Size < o.MinClipBytes {
			continue
		}
		if !o.CarIdle && f.MTime.After(cutoff) {
			continue
		}
		if ix.Has(vol, f.Rel, f.Size) {
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := priority[out[i].Category], priority[out[j].Category]
		if pi != pj {
			return pi < pj
		}
		return out[i].Rel < out[j].Rel // los nombres empiezan por fecha: orden cronológico
	})
	return out
}

var recentName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})_`)

// DestRel traduce la ruta del pendrive a la del NAS. TeslaCam/ se quita (el destino ya
// es la carpeta TeslaCam del recurso) y RecentClips se reparte por días, porque en el
// NAS se acumula sin límite y una carpeta plana con decenas de miles de ficheros no se puede navegar.
func DestRel(rel string) string {
	r := strings.TrimPrefix(rel, "TeslaCam/")
	dir, name := path.Split(r)
	if strings.HasSuffix(dir, "RecentClips/") {
		if m := recentName.FindStringSubmatch(name); m != nil {
			return dir + m[1] + "/" + name
		}
	}
	return r
}

// Progress se lee desde la API mientras se copia.
type Progress struct {
	TotalFiles int64
	TotalBytes int64
	DoneFiles  atomic.Int64
	DoneBytes  atomic.Int64
	Current    atomic.Value // string
	Started    time.Time
}

type CopyResult struct {
	Files int
	Bytes int64
}

// Copy copia items de srcRoot a dstRoot. Cada fichero se escribe como .part, se
// sincroniza, se comprueba el tamaño y solo entonces se renombra y se anota en el índice:
// un corte de corriente pierde como mucho el fichero en curso. Todo lo que se toca en el
// destino pasa por os.Root: un enlace simbólico del servidor no saca la escritura del NAS.
func Copy(ctx context.Context, srcRoot, dstRoot string, items []teslacam.File, ix *Index, vol string, p *Progress) (CopyResult, error) {
	var res CopyResult
	dst, err := os.OpenRoot(dstRoot)
	if err != nil {
		return res, err
	}
	defer dst.Close()
	buf := make([]byte, 1<<20)
	for _, f := range items {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		p.Current.Store(f.Rel)
		rel := filepath.FromSlash(DestRel(f.Rel))
		src := filepath.Join(srcRoot, filepath.FromSlash(f.Rel))
		// Si ya está en el NAS y es idéntico (índice perdido o pasada anterior cortada
		// justo tras el rename), solo se anota. El tamaño no basta: este fichero puede
		// acabar siendo la única copia cuando se libere espacio en el pendrive.
		same, err := sameContent(ctx, src, dst, rel, f.Size)
		if err != nil {
			return res, fmt.Errorf("%s: %w", f.Rel, err)
		}
		if !same {
			if err := copyOne(ctx, src, dst, rel, f.Size, buf, p); err != nil {
				return res, fmt.Errorf("%s: %w", f.Rel, err)
			}
			res.Bytes += f.Size
			res.Files++
		}
		if err := ix.Mark(vol, f.Rel, f.Size); err != nil {
			return res, err
		}
		p.DoneFiles.Add(1)
	}
	p.Current.Store("")
	return res, nil
}

var errSize = errors.New("copied size differs from the source")

func copyOne(ctx context.Context, src string, dst *os.Root, rel string, size int64, buf []byte, p *Progress) (err error) {
	if err := dst.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	part := rel + ".part"
	out, err := dst.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			out.Close()
			dst.Remove(part)
		}
	}()
	n, err := io.CopyBuffer(out, &ctxReader{ctx: ctx, r: in, p: p}, buf)
	if err != nil {
		return err
	}
	if n != size {
		return errSize
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = dst.Rename(part, rel); err != nil {
		return err
	}
	st, err := dst.Stat(rel)
	if err != nil {
		return err
	}
	if st.Size() != size {
		return errSize
	}
	return nil
}

// sameContent dice si rel existe en dst con el mismo contenido que src. Que falte, o que
// sea distinto, no es un error; los fallos de lectura sí lo son.
func sameContent(ctx context.Context, src string, dst *os.Root, rel string, size int64) (bool, error) {
	st, err := dst.Stat(rel)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if st.IsDir() || st.Size() != size {
		return false, nil
	}
	a, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer a.Close()
	b, err := dst.Open(rel)
	if err != nil {
		return false, err
	}
	defer b.Close()
	ba, bb := make([]byte, 1<<20), make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		na, ea := io.ReadFull(a, ba)
		nb, eb := io.ReadFull(b, bb)
		if !bytes.Equal(ba[:na], bb[:nb]) {
			return false, nil
		}
		eofA := ea == io.EOF || ea == io.ErrUnexpectedEOF
		eofB := eb == io.EOF || eb == io.ErrUnexpectedEOF
		if eofA && eofB {
			return true, nil
		}
		if ea != nil && !eofA {
			return false, ea
		}
		if eb != nil && !eofB {
			return false, eb
		}
		if eofA != eofB {
			return false, nil
		}
	}
}

// Verify compara con el NAS lo que se va a borrar del pendrive y devuelve solo lo que se
// puede borrar: carpetas enteras cuyos ficheros están idénticos en el destino. Lo que no
// coincide se olvida en el índice para que la siguiente pasada lo vuelva a copiar.
func Verify(ctx context.Context, srcRoot, dstRoot string, files []teslacam.File, rels []string, ix *Index, vol string) (keep []string, freed int64, err error) {
	dst, err := os.OpenRoot(dstRoot)
	if err != nil {
		return nil, 0, err
	}
	defer dst.Close()
	size := map[string]int64{}
	for _, f := range files {
		size[f.Rel] = f.Size
	}
	var bad []string
	badDir := map[string]bool{}
	for _, r := range rels {
		same, err := sameContent(ctx, filepath.Join(srcRoot, filepath.FromSlash(r)), dst, filepath.FromSlash(DestRel(r)), size[r])
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", r, err)
		}
		if !same {
			bad = append(bad, r)
			badDir[path.Dir(r)] = true
		}
	}
	for _, r := range rels {
		if !badDir[path.Dir(r)] {
			keep = append(keep, r)
			freed += size[r]
		}
	}
	if len(bad) > 0 {
		if err := ix.Forget(vol, bad); err != nil {
			return nil, 0, err
		}
	}
	return keep, freed, nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
	p   *Progress
}

func (c *ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.Read(b)
	c.p.DoneBytes.Add(int64(n))
	return n, err
}

// CleanParts borra .part huérfanos que hayan quedado en el NAS tras un corte.
func CleanParts(dstRoot string) (int, error) {
	n := 0
	err := filepath.WalkDir(dstRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(p, ".part") {
			if os.Remove(p) == nil {
				n++
			}
		}
		return nil
	})
	return n, err
}

// Deletable elige carpetas de eventos (Saved/Sentry/Track) ya archivadas por completo,
// de la más antigua a la más nueva, hasta liberar want bytes. RecentClips nunca: lo
// rota el propio coche.
func Deletable(files []teslacam.File, ix *Index, vol string, want int64) (rels []string, freed int64) {
	type group struct {
		files []teslacam.File
		bytes int64
		ok    bool
	}
	groups := map[string]*group{}
	var order []string
	for _, f := range files {
		if f.Category != teslacam.Saved && f.Category != teslacam.Sentry && f.Category != teslacam.Track {
			continue
		}
		dir := path.Dir(f.Rel)
		g := groups[dir]
		if g == nil {
			g = &group{ok: true}
			groups[dir] = g
			order = append(order, dir)
		}
		g.files = append(g.files, f)
		g.bytes += f.Size
		if !ix.Has(vol, f.Rel, f.Size) {
			g.ok = false
		}
	}
	sort.Strings(order)
	for _, dir := range order {
		if freed >= want {
			break
		}
		g := groups[dir]
		if !g.ok {
			continue
		}
		for _, f := range g.files {
			rels = append(rels, f.Rel)
		}
		freed += g.bytes
	}
	return rels, freed
}
