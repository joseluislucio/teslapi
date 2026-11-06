package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LogEntry es una línea de log para el portal.
type LogEntry struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}

// Ring guarda las últimas líneas en memoria (journald es volátil y el portal no
// debe depender de journalctl) y las reenvía al handler normal.
type Ring struct {
	next  slog.Handler
	mu    *sync.Mutex
	buf   *[]LogEntry
	attrs string
}

const ringSize = 500

func NewRing(next slog.Handler) *Ring {
	b := make([]LogEntry, 0, ringSize)
	return &Ring{next: next, mu: &sync.Mutex{}, buf: &b}
}

func (r *Ring) Enabled(ctx context.Context, l slog.Level) bool { return r.next.Enabled(ctx, l) }

func (r *Ring) Handle(ctx context.Context, rec slog.Record) error {
	var sb strings.Builder
	sb.WriteString(rec.Message)
	sb.WriteString(r.attrs)
	rec.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value)
		return true
	})
	r.mu.Lock()
	if len(*r.buf) >= ringSize {
		*r.buf = append((*r.buf)[:0], (*r.buf)[1:]...)
	}
	*r.buf = append(*r.buf, LogEntry{Time: rec.Time, Level: rec.Level.String(), Msg: sb.String()})
	r.mu.Unlock()
	return r.next.Handle(ctx, rec)
}

func (r *Ring) WithAttrs(as []slog.Attr) slog.Handler {
	c := *r
	var sb strings.Builder
	sb.WriteString(r.attrs)
	for _, a := range as {
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value)
	}
	c.attrs = sb.String()
	c.next = r.next.WithAttrs(as)
	return &c
}

func (r *Ring) WithGroup(name string) slog.Handler {
	c := *r
	c.next = r.next.WithGroup(name)
	return &c
}

func (r *Ring) Entries() []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]LogEntry(nil), *r.buf...)
}
