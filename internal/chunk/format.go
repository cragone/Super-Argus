// Package chunk implements the on-disk chunk format, the in-memory head chunk
// and the Store abstraction.
//
// File layout (big endian):
//
//	header : "ARGC" | version u8 | streamID u64 | minTS i64 | maxTS i64
//	blocks : zstd( repeated [ts delta varint | len uvarint | bytes] )
//	index  : per block [offset u64 | compLen u32 | lines u32 | minTS i64 | maxTS i64]
//	blooms : (version 2) per block [len u32 | trigram bloom bytes], see bloom.go
//	trailer: numBlocks u32 | indexOffset u64 | "ARGE"
//
// Version 1 chunks (no blooms) remain readable.
package chunk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"github.com/klauspost/compress/zstd"
)

// Entry is one log line with a unix-nanosecond timestamp.
type Entry struct {
	TS   int64
	Line string
}

// Meta summarises an encoded chunk.
type Meta struct {
	StreamID        uint64
	MinTS, MaxTS    int64
	Lines           int
	BytesRaw        int
	BytesCompressed int
}

// BlockInfo is one block's entry in the footer index.
type BlockInfo struct {
	Offset       uint64
	CompLen      uint32
	Lines        uint32
	MinTS, MaxTS int64
}

const (
	magic          = "ARGC"
	footerMagic    = "ARGE"
	version        = 2
	versionNoBloom = 1
	headerSize     = 4 + 1 + 8 + 8 + 8
	indexEntrySz   = 8 + 4 + 4 + 8 + 8
	trailerSize    = 4 + 8 + 4
	maxDecodedBlk  = 64 << 20
)

// TargetBlockBytes is the raw size at which a block is closed.
var TargetBlockBytes = 256 << 10

// ErrCorrupt is returned for malformed chunk data.
var ErrCorrupt = errors.New("chunk: corrupt data")

var (
	// Blocks are ~256 KiB and compressed independently, so a 1 MiB window loses
	// nothing, while the default 8 MiB window made every concurrent encoder pin
	// tens of MiB of history (the soak test measured ~250 MB held that way).
	zenc, _ = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(4), zstd.WithWindowSize(1<<20), zstd.WithLowerEncoderMem(true))
	zdec, _ = zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecodedBlk))
)

// Encode builds a chunk file image from entries (sorted by timestamp, stable).
func Encode(streamID uint64, entries []Entry) ([]byte, Meta, error) {
	if len(entries) == 0 {
		return nil, Meta{}, errors.New("chunk: no entries")
	}
	es := make([]Entry, len(entries))
	copy(es, entries)
	sort.SliceStable(es, func(i, j int) bool { return es[i].TS < es[j].TS })

	m := Meta{StreamID: streamID, MinTS: es[0].TS, MaxTS: es[len(es)-1].TS, Lines: len(es)}
	out := make([]byte, headerSize, 1<<16)
	copy(out, magic)
	out[4] = version
	binary.BigEndian.PutUint64(out[5:], streamID)
	binary.BigEndian.PutUint64(out[13:], uint64(m.MinTS))
	binary.BigEndian.PutUint64(out[21:], uint64(m.MaxTS))

	var blocks []BlockInfo
	var blooms []bloom
	tg := trigramPool.Get().(*trigramSet)
	defer trigramPool.Put(tg)
	var raw []byte
	var cur BlockInfo
	var prev int64
	flush := func() {
		if cur.Lines == 0 {
			return
		}
		comp := zenc.EncodeAll(raw, nil)
		cur.Offset = uint64(len(out))
		cur.CompLen = uint32(len(comp))
		out = append(out, comp...)
		blocks = append(blocks, cur)
		blooms = append(blooms, tg.build())
		m.BytesRaw += len(raw)
		raw, cur, prev = raw[:0], BlockInfo{}, 0
	}
	for _, e := range es {
		if cur.Lines == 0 {
			cur.MinTS = e.TS
		}
		cur.MaxTS = e.TS
		cur.Lines++
		raw = binary.AppendVarint(raw, e.TS-prev)
		prev = e.TS
		raw = binary.AppendUvarint(raw, uint64(len(e.Line)))
		raw = append(raw, e.Line...)
		tg.add(e.Line)
		if len(raw) >= TargetBlockBytes {
			flush()
		}
	}
	flush()

	indexOff := uint64(len(out))
	for _, b := range blocks {
		out = binary.BigEndian.AppendUint64(out, b.Offset)
		out = binary.BigEndian.AppendUint32(out, b.CompLen)
		out = binary.BigEndian.AppendUint32(out, b.Lines)
		out = binary.BigEndian.AppendUint64(out, uint64(b.MinTS))
		out = binary.BigEndian.AppendUint64(out, uint64(b.MaxTS))
	}
	for _, b := range blooms {
		out = appendBloom(out, b)
	}
	out = binary.BigEndian.AppendUint32(out, uint32(len(blocks)))
	out = binary.BigEndian.AppendUint64(out, indexOff)
	out = append(out, footerMagic...)
	m.BytesCompressed = len(out)
	return out, m, nil
}

// Reader reads a chunk image.
type Reader struct {
	data   []byte
	Meta   Meta
	Blocks []BlockInfo
	blooms []bloom // nil for version 1 chunks
}

// NewReader parses the header and footer index of data.
func NewReader(data []byte) (*Reader, error) {
	if len(data) < headerSize+trailerSize || string(data[:4]) != magic ||
		(data[4] != version && data[4] != versionNoBloom) || string(data[len(data)-4:]) != footerMagic {
		return nil, ErrCorrupt
	}
	r := &Reader{data: data}
	r.Meta.StreamID = binary.BigEndian.Uint64(data[5:])
	r.Meta.MinTS = int64(binary.BigEndian.Uint64(data[13:]))
	r.Meta.MaxTS = int64(binary.BigEndian.Uint64(data[21:]))
	t := data[len(data)-trailerSize:]
	n := uint64(binary.BigEndian.Uint32(t))
	idxOff := binary.BigEndian.Uint64(t[4:])
	end := uint64(len(data) - trailerSize)
	if idxOff < headerSize || idxOff+n*indexEntrySz > end {
		return nil, ErrCorrupt
	}
	idxEnd := idxOff + n*indexEntrySz
	if data[4] == versionNoBloom {
		if idxEnd != end {
			return nil, ErrCorrupt
		}
	} else {
		bl, ok := parseBlooms(data[idxEnd:end], int(n))
		if !ok {
			return nil, ErrCorrupt
		}
		r.blooms = bl
	}
	for i := 0; i < int(n); i++ {
		e := data[int(idxOff)+i*indexEntrySz:]
		b := BlockInfo{
			Offset:  binary.BigEndian.Uint64(e),
			CompLen: binary.BigEndian.Uint32(e[8:]),
			Lines:   binary.BigEndian.Uint32(e[12:]),
			MinTS:   int64(binary.BigEndian.Uint64(e[16:])),
			MaxTS:   int64(binary.BigEndian.Uint64(e[24:])),
		}
		if b.Offset < headerSize || b.Offset+uint64(b.CompLen) > idxOff {
			return nil, ErrCorrupt
		}
		r.Blocks = append(r.Blocks, b)
		r.Meta.Lines += int(b.Lines)
	}
	return r, nil
}

// Scan calls fn for each entry with from <= ts < to, in timestamp order,
// skipping blocks outside the range. fn returns false to stop early.
func (r *Reader) Scan(from, to int64, fn func(Entry) bool) error {
	return r.ScanContaining(from, to, nil, fn)
}

// ScanContaining is Scan that also skips blocks whose bloom proves they lack
// one of the needles. Every needle must appear in a line for it to matter, so
// callers pass the substrings of their `|=` filters. Lines inside surviving
// blocks are not filtered; fn still sees them all.
func (r *Reader) ScanContaining(from, to int64, needles []string, fn func(Entry) bool) error {
	for bi, b := range r.Blocks {
		if b.MaxTS < from || b.MinTS >= to {
			continue
		}
		if r.blooms != nil && !mayContainAll(r.blooms[bi], needles) {
			continue
		}
		raw, err := zdec.DecodeAll(r.data[b.Offset:b.Offset+uint64(b.CompLen)], nil)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		var ts int64
		for i := uint32(0); i < b.Lines; i++ {
			d, n := binary.Varint(raw)
			if n <= 0 {
				return ErrCorrupt
			}
			raw = raw[n:]
			l, n := binary.Uvarint(raw)
			if n <= 0 || uint64(len(raw)-n) < l {
				return ErrCorrupt
			}
			ts += d
			line := raw[n : n+int(l)]
			raw = raw[n+int(l):]
			if ts < from || ts >= to {
				continue
			}
			if !fn(Entry{TS: ts, Line: string(line)}) {
				return nil
			}
		}
	}
	return nil
}

func mayContainAll(b bloom, needles []string) bool {
	for _, n := range needles {
		if !b.mayContain(n) {
			return false
		}
	}
	return true
}
