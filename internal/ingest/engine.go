// Package ingest accepts pushed log lines: validation, limits, the write path
// (WAL -> head chunks -> chunk files) and the HTTP push handler.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/index"
	"github.com/cragone/Super-Argus/internal/metrics"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/cragone/Super-Argus/internal/wal"
	"github.com/google/uuid"
)

// EngineConfig tunes head-chunk flushing.
type EngineConfig struct {
	MaxHeadBytes int           // flush a head once it holds this much raw data (~1 MB)
	MaxHeadAge   time.Duration // flush a head this long after its first line (~1h)
	TickEvery    time.Duration // how often Run checks heads and persists last_seen
	Now          func() time.Time
	Log          *slog.Logger
	Metrics      *metrics.Set // optional
}

// DefaultEngineConfig returns the production defaults.
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{MaxHeadBytes: 1 << 20, MaxHeadAge: time.Hour, TickEvery: time.Second}
}

// Engine owns the write path. Append is durable once it returns (subject to
// the WAL sync interval); flushed heads become chunk files plus index rows.
// Publisher receives every accepted batch (the live-tail hub). Publish must never block.
type Publisher interface{ Publish([]wal.Record) }

type Engine struct {
	pub   Publisher
	cfg   EngineConfig
	wal   *wal.WAL
	ix    *index.Index
	store chunk.Store

	mu       sync.Mutex
	heads    map[uint64]*chunk.Head
	flushing map[*chunk.Head]struct{}
}

// NewEngine builds an engine. Call Recover before accepting traffic.
func NewEngine(w *wal.WAL, ix *index.Index, store chunk.Store, cfg EngineConfig) *Engine {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.MaxHeadBytes <= 0 {
		cfg.MaxHeadBytes = 1 << 20
	}
	if cfg.MaxHeadAge <= 0 {
		cfg.MaxHeadAge = time.Hour
	}
	if cfg.TickEvery <= 0 {
		cfg.TickEvery = time.Second
	}
	return &Engine{cfg: cfg, wal: w, ix: ix, store: store,
		heads: map[uint64]*chunk.Head{}, flushing: map[*chunk.Head]struct{}{}}
}

// SetPublisher registers the live-tail publisher. Call before accepting traffic.
func (e *Engine) SetPublisher(p Publisher) { e.pub = p }

// Recover replays the WAL left by a previous run into chunk files, then drops
// the replayed segments. A crash between writing chunks and dropping segments
// can duplicate lines on the next start (at-least-once).
func (e *Engine) Recover() error {
	e.mu.Lock()
	st, err := e.wal.Replay(func(seg uint64, r wal.Record) {
		e.addLocked(seg, []wal.Record{r})
	})
	e.mu.Unlock()
	if err != nil {
		return fmt.Errorf("wal replay: %w", err)
	}
	if st.Records > 0 || st.Torn > 0 {
		e.cfg.Log.Info("wal replayed", "records", st.Records, "torn_segments", st.Torn)
	}
	if err := e.FlushAll(); err != nil {
		return err
	}
	return e.checkpoint() // also drops replayed segments when there was nothing to flush
}

// Append writes recs to the WAL and then to the in-memory heads.
func (e *Engine) Append(recs []wal.Record) error {
	if len(recs) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	seg, err := e.wal.Append(recs)
	if err != nil {
		return err
	}
	e.addLocked(seg, recs)
	if e.pub != nil {
		e.pub.Publish(recs) // non-blocking; under the lock so tails see WAL order
	}
	return nil
}

func (e *Engine) addLocked(seg uint64, recs []wal.Record) {
	now := e.cfg.Now()
	for _, r := range recs {
		h := e.heads[r.StreamID]
		if h == nil {
			h = &chunk.Head{StreamID: r.StreamID, ID: uuid.NewString()}
			e.heads[r.StreamID] = h
		}
		h.Add(seg, now, chunk.Entry{TS: r.TS, Line: r.Line})
	}
}

// HeadLines returns the number of unflushed lines (for tests and metrics).
func (e *Engine) HeadLines() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, h := range e.heads {
		n += len(h.Entries)
	}
	return n
}

// SnapshotHeads implements chunk.HeadSource.
func (e *Engine) SnapshotHeads(streams map[uint64]struct{}, from, to int64) []chunk.HeadSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []chunk.HeadSnapshot
	snap := func(h *chunk.Head) {
		if streams != nil {
			if _, ok := streams[h.StreamID]; !ok {
				return
			}
		}
		var es []chunk.Entry
		for _, en := range h.Entries {
			if en.TS >= from && en.TS < to {
				es = append(es, en)
			}
		}
		if len(es) > 0 {
			out = append(out, chunk.HeadSnapshot{StreamID: h.StreamID, ChunkID: h.ID, Entries: es})
		}
	}
	for _, h := range e.heads {
		snap(h)
	}
	for h := range e.flushing {
		snap(h)
	}
	return out
}

// Tick flushes heads that are full or old, persists last_seen and trims the WAL.
func (e *Engine) Tick() error {
	now := e.cfg.Now()
	err := e.flush(func(h *chunk.Head) bool {
		return h.RawBytes >= e.cfg.MaxHeadBytes || now.Sub(h.Created) >= e.cfg.MaxHeadAge
	})
	return errors.Join(err, e.ix.FlushSeen())
}

// FlushAll flushes every head (used on shutdown and after recovery).
func (e *Engine) FlushAll() error {
	err := e.flush(func(*chunk.Head) bool { return true })
	return errors.Join(err, e.ix.FlushSeen())
}

func (e *Engine) flush(pred func(*chunk.Head) bool) error {
	e.mu.Lock()
	var taken []*chunk.Head
	for id, h := range e.heads {
		if pred(h) {
			taken = append(taken, h)
			delete(e.heads, id)
			e.flushing[h] = struct{}{}
		}
	}
	e.mu.Unlock()

	began := time.Now()
	var errs []error
	failed := 0
	for _, h := range taken {
		err := e.writeChunk(h)
		e.mu.Lock()
		delete(e.flushing, h)
		if err != nil {
			errs = append(errs, err)
			failed++
			e.restoreLocked(h)
		}
		e.mu.Unlock()
	}
	if len(taken) > 0 {
		errs = append(errs, e.checkpoint())
		e.cfg.Metrics.Flush(len(taken)-failed, time.Since(began), errors.Join(errs...))
	}
	return errors.Join(errs...)
}

// restoreLocked puts a head whose flush failed back so it is retried.
func (e *Engine) restoreLocked(h *chunk.Head) {
	if cur := e.heads[h.StreamID]; cur != nil {
		h.Entries = append(h.Entries, cur.Entries...)
		h.RawBytes += cur.RawBytes
	}
	e.heads[h.StreamID] = h
}

func (e *Engine) writeChunk(h *chunk.Head) error {
	data, meta, err := chunk.Encode(h.StreamID, h.Entries)
	if err != nil {
		return err
	}
	id := h.ID
	key := chunk.Key(meta.MinTS, h.StreamID, id)
	if err := e.store.Put(key, data); err != nil {
		return fmt.Errorf("store chunk: %w", err)
	}
	row := &models.Chunk{ID: id, StreamID: h.StreamID, Path: key, MinTS: meta.MinTS, MaxTS: meta.MaxTS,
		LineCount: meta.Lines, BytesCompressed: int64(meta.BytesCompressed), BytesRaw: int64(meta.BytesRaw)}
	if err := e.ix.AddChunk(row); err != nil {
		_ = e.store.Delete(key)
		return fmt.Errorf("index chunk: %w", err)
	}
	return nil
}

// checkpoint drops WAL segments that no unflushed line depends on.
func (e *Engine) checkpoint() error {
	e.mu.Lock()
	floor := e.wal.CurrentSeq()
	idle := len(e.heads) == 0 && len(e.flushing) == 0
	for _, h := range e.heads {
		floor = min(floor, h.FirstSeg)
	}
	for h := range e.flushing {
		floor = min(floor, h.FirstSeg)
	}
	if idle {
		// Everything is flushed: roll the active segment so it can be dropped too.
		seq, err := e.wal.Rotate()
		if err != nil {
			e.mu.Unlock()
			return err
		}
		floor = seq
	}
	e.mu.Unlock()
	return e.wal.TruncateBefore(floor)
}

// Run ticks until ctx is cancelled. It does not flush on exit; call Close for that.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(e.cfg.TickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := e.Tick(); err != nil {
				e.cfg.Log.Error("flush", "err", err)
			}
		}
	}
}

// Close flushes all heads and closes the WAL.
func (e *Engine) Close() error {
	err := e.FlushAll()
	return errors.Join(err, e.wal.Close())
}
