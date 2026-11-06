package archive

import (
	"teslapi/internal/msg"

	"encoding/binary"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Index recuerda qué ficheros de cada formateo del pendrive ya están en el NAS.
// Clave = ruta relativa, valor = tamaño. Si el coche reescribe un fichero con otro
// tamaño, deja de contar como archivado y se vuelve a copiar.
type Index struct {
	db *bolt.DB
}

var (
	bHistory = []byte("history")
	bPrefix  = "archived/"
)

func OpenIndex(path string) (*Index, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bHistory)
		return err
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Index{db: db}, nil
}

func (ix *Index) Close() error { return ix.db.Close() }

func bucket(vol string) []byte { return []byte(bPrefix + vol) }

func (ix *Index) Has(vol, rel string, size int64) bool {
	var ok bool
	ix.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket(vol))
		if b == nil {
			return nil
		}
		v := b.Get([]byte(rel))
		ok = len(v) == 8 && int64(binary.BigEndian.Uint64(v)) == size
		return nil
	})
	return ok
}

func (ix *Index) Mark(vol, rel string, size int64) error {
	return ix.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucket(vol))
		if err != nil {
			return err
		}
		v := make([]byte, 8)
		binary.BigEndian.PutUint64(v, uint64(size))
		return b.Put([]byte(rel), v)
	})
}

// Forget quita entradas (tras borrar los ficheros del pendrive).
func (ix *Index) Forget(vol string, rels []string) error {
	return ix.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket(vol))
		if b == nil {
			return nil
		}
		for _, r := range rels {
			if err := b.Delete([]byte(r)); err != nil {
				return err
			}
		}
		return nil
	})
}

// PruneVolumes borra los índices de formateos anteriores: si el coche ha
// formateado, esos ficheros ya no existen en el pendrive.
func (ix *Index) PruneVolumes(keep string) error {
	return ix.db.Update(func(tx *bolt.Tx) error {
		var drop [][]byte
		tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
			n := string(name)
			if len(n) > len(bPrefix) && n[:len(bPrefix)] == bPrefix && n != bPrefix+keep {
				drop = append(drop, append([]byte(nil), name...))
			}
			return nil
		})
		for _, n := range drop {
			if err := tx.DeleteBucket(n); err != nil {
				return err
			}
		}
		return nil
	})
}

// Run es una pasada de archivado, para el historial del portal.
type Run struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Volume   string    `json:"volume"`
	Pending  int       `json:"pending"`
	Files    int       `json:"files"`
	Bytes    int64     `json:"bytes"`
	Freed    int64     `json:"freed"`
	Result   string    `json:"result"` // ok | interrupted | error
	Message  msg.Msg   `json:"message"`
	LeftOver int       `json:"left_over"`
}

const maxHistory = 200

func (ix *Index) AddRun(r Run) error {
	return ix.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bHistory)
		v, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err := b.Put([]byte(r.Start.UTC().Format(time.RFC3339Nano)), v); err != nil {
			return err
		}
		for b.Stats().KeyN > maxHistory {
			k, _ := b.Cursor().First()
			if k == nil {
				break
			}
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func (ix *Index) History(limit int) []Run {
	var out []Run
	ix.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bHistory).Cursor()
		for k, v := c.Last(); k != nil && len(out) < limit; k, v = c.Prev() {
			var r Run
			if json.Unmarshal(v, &r) == nil {
				out = append(out, r)
			}
		}
		return nil
	})
	return out
}
