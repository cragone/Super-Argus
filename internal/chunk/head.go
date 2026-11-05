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
}

// Add appends an entry; seg is the WAL segment it was written to.
func (h *Head) Add(seg uint64, now time.Time, e Entry) {
	if len(h.Entries) == 0 {
		h.Created, h.FirstSeg = now, seg
	}
	h.Entries = append(h.Entries, e)
	h.RawBytes += len(e.Line) + 16
}
