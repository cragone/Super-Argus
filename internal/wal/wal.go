// Package wal implements a segmented write-ahead log for ingested log lines.
//
// Record framing: len u32 | crc32(payload) u32 | payload, where payload is
// streamID u64 | ts i64 | line. A torn or corrupt record ends replay of its segment.
package wal

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Record is one WAL entry.
type Record struct {
	StreamID uint64
	TS       int64
	Line     string
}

const (
	segPrefix = "wal-"
	segSuffix = ".log"
	maxRecord = 16 << 20
)

// DefaultSegmentSize is the size at which the active segment rotates.
const DefaultSegmentSize = 64 << 20

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// WAL is a segmented append-only log.
type WAL struct {
	dir         string
	SegmentSize int64

	mu      sync.Mutex
	f       *os.File
	seq     uint64
	size    int64
	dirty   bool
	strict  bool
	closed  bool
	old     []uint64 // segments present at Open, awaiting Replay/Truncate
	stop    chan struct{}
	stopped chan struct{}
}

// Open opens dir and starts a fresh segment. syncEvery > 0 fsyncs on that
// interval; syncEvery == 0 is strict mode (fsync on every Append).
func Open(dir string, syncEvery time.Duration) (*WAL, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	segs, err := listSegments(dir)
	if err != nil {
		return nil, err
	}
	w := &WAL{dir: dir, SegmentSize: DefaultSegmentSize, strict: syncEvery <= 0, old: segs}
	if len(segs) > 0 {
		w.seq = segs[len(segs)-1]
	}
	if err := w.openNext(); err != nil {
		return nil, err
	}
	if !w.strict {
		w.stop, w.stopped = make(chan struct{}), make(chan struct{})
		go w.syncLoop(syncEvery)
	}
	return w, nil
}

func segName(dir string, seq uint64) string {
	return filepath.Join(dir, fmt.Sprintf("%s%08d%s", segPrefix, seq, segSuffix))
}

func listSegments(dir string) ([]uint64, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var segs []uint64
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, segPrefix) || !strings.HasSuffix(n, segSuffix) {
			continue
		}
		if s, err := strconv.ParseUint(n[len(segPrefix):len(n)-len(segSuffix)], 10, 64); err == nil {
			segs = append(segs, s)
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i] < segs[j] })
	return segs, nil
}

// openNext starts a new segment. Caller holds mu (or is Open).
func (w *WAL) openNext() error {
	w.seq++
	f, err := os.OpenFile(segName(w.dir, w.seq), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w.f, w.size, w.dirty = f, 0, false
	return nil
}

func (w *WAL) syncLoop(every time.Duration) {
	defer close(w.stopped)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = w.Sync()
		case <-w.stop:
			return
		}
	}
}

// Sync fsyncs the active segment if it has unsynced writes.
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.syncLocked()
}

func (w *WAL) syncLocked() error {
	if w.closed || !w.dirty {
		return nil
	}
	w.dirty = false
	return w.f.Sync()
}

// Append writes recs in one write and returns the segment they landed in.
func (w *WAL) Append(recs []Record) (uint64, error) {
	if len(recs) == 0 {
		return 0, errors.New("wal: empty append")
	}
	var buf []byte
	for _, r := range recs {
		n := 16 + len(r.Line)
		if n > maxRecord {
			return 0, errors.New("wal: record too large")
		}
		buf = binary.BigEndian.AppendUint32(buf, uint32(n))
		crcAt := len(buf)
		buf = binary.BigEndian.AppendUint32(buf, 0)
		buf = binary.BigEndian.AppendUint64(buf, r.StreamID)
		buf = binary.BigEndian.AppendUint64(buf, uint64(r.TS))
		buf = append(buf, r.Line...)
		binary.BigEndian.PutUint32(buf[crcAt:], crc32.Checksum(buf[crcAt+4:], crcTable))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errors.New("wal: closed")
	}
	if _, err := w.f.Write(buf); err != nil {
		return 0, err
	}
	seg := w.seq
	w.size += int64(len(buf))
	w.dirty = true
	if w.strict {
		if err := w.syncLocked(); err != nil {
			return 0, err
		}
	}
	if w.size >= w.SegmentSize {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	return seg, nil
}

func (w *WAL) rotateLocked() error {
	if err := w.syncLocked(); err != nil {
		return err
	}
	if err := w.f.Close(); err != nil {
		return err
	}
	return w.openNext()
}

// Rotate starts a new segment if the active one is non-empty and returns the active sequence.
func (w *WAL) Rotate() (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.seq, errors.New("wal: closed")
	}
	if w.size > 0 {
		if err := w.rotateLocked(); err != nil {
			return w.seq, err
		}
	}
	return w.seq, nil
}

// CurrentSeq returns the active segment sequence.
func (w *WAL) CurrentSeq() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq
}

// DiskUsage returns the number of segment files and their total size.
func (w *WAL) DiskUsage() (segments int, bytes int64) {
	ents, err := os.ReadDir(w.dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), segPrefix) || !strings.HasSuffix(e.Name(), segSuffix) {
			continue
		}
		if fi, err := e.Info(); err == nil {
			segments++
			bytes += fi.Size()
		}
	}
	return segments, bytes
}

// ReplayStats reports what Replay saw.
type ReplayStats struct {
	Records int
	// Torn counts segments that ended in a partial or corrupt record.
	Torn int
}

// Replay calls fn for every record in the segments that existed at Open, oldest first.
// Segments written by this process are not replayed.
func (w *WAL) Replay(fn func(seg uint64, r Record)) (ReplayStats, error) {
	w.mu.Lock()
	segs := append([]uint64(nil), w.old...)
	w.mu.Unlock()
	var st ReplayStats
	for _, seg := range segs {
		torn, n, err := replaySegment(segName(w.dir, seg), func(r Record) { fn(seg, r) })
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return st, err
		}
		st.Records += n
		if torn {
			st.Torn++
		}
	}
	return st, nil
}

func replaySegment(path string, fn func(Record)) (torn bool, n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, 0, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if err == io.EOF {
				return false, n, nil
			}
			return true, n, nil // partial header
		}
		l := binary.BigEndian.Uint32(hdr[:4])
		if l < 16 || l > maxRecord {
			return true, n, nil
		}
		payload := make([]byte, l)
		if _, err := io.ReadFull(br, payload); err != nil {
			return true, n, nil
		}
		if crc32.Checksum(payload, crcTable) != binary.BigEndian.Uint32(hdr[4:]) {
			return true, n, nil
		}
		fn(Record{
			StreamID: binary.BigEndian.Uint64(payload),
			TS:       int64(binary.BigEndian.Uint64(payload[8:])),
			Line:     string(payload[16:]),
		})
		n++
	}
}

// TruncateBefore deletes segments with sequence < seq. The active segment is never removed.
func (w *WAL) TruncateBefore(seq uint64) error {
	w.mu.Lock()
	active := w.seq
	w.mu.Unlock()
	segs, err := listSegments(w.dir)
	if err != nil {
		return err
	}
	for _, s := range segs {
		if s < seq && s != active {
			if err := os.Remove(segName(w.dir, s)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// Close syncs and closes the log.
func (w *WAL) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	err := w.syncLocked()
	w.closed = true
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	w.mu.Unlock()
	if w.stop != nil {
		close(w.stop)
		<-w.stopped
	}
	return err
}
