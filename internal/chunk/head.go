package chunk

import "time"

// Head is the in-memory, not yet flushed chunk of one stream.
type Head struct {
	StreamID uint64
	Entries  []Entry
	RawBytes int
	Created  time.Time
	// FirstSeg is the WAL segment holding this head's oldest entry.
	FirstSeg uint64
	// ID becomes the chunk ID when the head is flushed, so a reader that
	// snapshots heads and lists chunks can drop heads that already became chunks.
	ID string
}

// Add appends an entry; seg is the WAL segment it was written to.
func (h *Head) Add(seg uint64, now time.Time, e Entry) {
	if len(h.Entries) == 0 {
		h.Created, h.FirstSeg = now, seg
	}
	h.Entries = append(h.Entries, e)
	h.RawBytes += len(e.Line) + 16
}

// HeadSnapshot is a read-only copy of a stream's unflushed lines.
type HeadSnapshot struct {
	StreamID uint64
	ChunkID  string // ID the head's chunk will have once flushed
	Entries  []Entry
}

// HeadSource exposes unflushed lines to the query engine.
type HeadSource interface {
	// SnapshotHeads copies lines with from <= ts < to for the given streams
	// (all streams when the set is nil), including heads that are mid-flush.
	SnapshotHeads(streams map[uint64]struct{}, from, to int64) []HeadSnapshot
}
