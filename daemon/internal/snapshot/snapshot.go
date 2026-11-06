// Package snapshot da al daemon una vista de solo lectura del pendrive sin
// desconectarlo del coche: copia por reflink (XFS, instantánea), reparación del FS
// sobre la COPIA y montaje en ro. El coche sigue escribiendo en la imagen original.
package snapshot

import (
	"context"
	"sync"
	"time"

	"teslapi/internal/diskimg"
)

type Mounted struct {
	Root       string         `json:"root"`
	Vol        diskimg.Volume `json:"volume"`
	CarIdle    bool           `json:"car_idle"`
	Taken      time.Time      `json:"taken"`
	FreeBytes  int64          `json:"free_bytes"`
	TotalBytes int64          `json:"total_bytes"`
	Repaired   bool           `json:"repaired"`

	once    sync.Once
	release func() error
	err     error
}

// Release desmonta y borra el snapshot. Se puede llamar varias veces.
func (m *Mounted) Release() error {
	if m == nil || m.release == nil {
		return nil
	}
	m.once.Do(func() { m.err = m.release() })
	return m.err
}

type Snapshotter interface {
	// Snapshot monta una copia de solo lectura del pendrive.
	Snapshot(ctx context.Context) (*Mounted, error)
	// Live monta la imagen real en lectura/escritura. Solo con el gadget desconectado.
	Live(ctx context.Context) (*Mounted, error)
	// Cleanup deshace snapshots que quedaran de una ejecución anterior (corte de corriente).
	Cleanup(ctx context.Context) error
}

// Fake sirve un directorio del Mac (.devdata) como si fuera el pendrive.
type Fake struct {
	Root string
}

func (f *Fake) Snapshot(context.Context) (*Mounted, error) {
	return &Mounted{
		Root: f.Root, Vol: diskimg.Volume{FSType: "exfat", Serial: 0xEFD9F94E, Offset: 1 << 20, Scheme: "mbr", Size: 92 << 30},
		CarIdle: true, Taken: time.Now(), TotalBytes: 92 << 30, FreeBytes: 61 << 30,
		release: func() error { return nil },
	}, nil
}

func (f *Fake) Live(ctx context.Context) (*Mounted, error) { return f.Snapshot(ctx) }
func (f *Fake) Cleanup(context.Context) error              { return nil }

// NewMounted construye un montaje a mano (pruebas y dobles de prueba).
func NewMounted(root string, vol diskimg.Volume, total, free int64, carIdle bool, release func() error) *Mounted {
	return &Mounted{Root: root, Vol: vol, TotalBytes: total, FreeBytes: free, CarIdle: carIdle,
		Taken: time.Now(), release: release}
}
