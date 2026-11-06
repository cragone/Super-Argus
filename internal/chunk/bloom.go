package chunk

import (
	"encoding/binary"
	"math/bits"
	"sync"
)

// Per-block bloom filters index every 3-byte window (trigram) of a block's
// lines. A substring filter of three or more bytes can only match a block
// whose bloom contains all of the needle's trigrams, so blocks that fail the
// test are skipped without being decompressed. False positives only cost a
// scan; there are no false negatives.
const (
	bloomBitsPerItem = 6
	bloomHashes      = 2
	bloomMinBits     = 4096
	bloomMaxBits     = 32 << 13 // 32 KiB
	// MinNeedle is the shortest substring the bloom can answer for.
	MinNeedle = 3
)

type bloom []uint64

func (b bloom) set(h uint64) {
	m := uint64(len(b)) * 64
	for i := 0; i < bloomHashes; i++ {
		x := (h + uint64(i)*0x9E3779B97F4A7C15)
		x ^= x >> 31
		x *= 0xBF58476D1CE4E5B9
		x ^= x >> 29
		bit := x % m
		b[bit>>6] |= 1 << (bit & 63)
	}
}

func (b bloom) has(h uint64) bool {
	m := uint64(len(b)) * 64
	for i := 0; i < bloomHashes; i++ {
		x := (h + uint64(i)*0x9E3779B97F4A7C15)
		x ^= x >> 31
		x *= 0xBF58476D1CE4E5B9
		x ^= x >> 29
		bit := x % m
		if b[bit>>6]&(1<<(bit&63)) == 0 {
			return false
		}
	}
	return true
}

func tri(s string, i int) uint32 { return uint32(s[i])<<16 | uint32(s[i+1])<<8 | uint32(s[i+2]) }

// trigramSet collects the distinct trigrams of a block (24-bit space = 2 MiB bitset).
type trigramSet struct {
	words []uint64
	n     int
}

var trigramPool = sync.Pool{New: func() any { return &trigramSet{words: make([]uint64, 1<<18)} }}

func (t *trigramSet) add(line string) {
	for i := 0; i+3 <= len(line); i++ {
		v := tri(line, i)
		w, m := v>>6, uint64(1)<<(v&63)
		if t.words[w]&m == 0 {
			t.words[w] |= m
			t.n++
		}
	}
}

// build sizes a bloom for the collected trigrams, fills it and resets the set.
// It returns nil when the block has no trigrams.
func (t *trigramSet) build() bloom {
	defer func() {
		clear(t.words)
		t.n = 0
	}()
	if t.n == 0 {
		return nil
	}
	nbits := min(max(t.n*bloomBitsPerItem, bloomMinBits), bloomMaxBits)
	b := make(bloom, (nbits+63)/64)
	for wi, w := range t.words {
		for w != 0 {
			bit := bits.TrailingZeros64(w)
			w &= w - 1
			b.set(uint64(wi*64 + bit))
		}
	}
	return b
}

// mayContain reports whether a block could contain needle. Needles shorter
// than MinNeedle, and blocks without a bloom, always answer true.
func (b bloom) mayContain(needle string) bool {
	if len(b) == 0 || len(needle) < MinNeedle {
		return true
	}
	for i := 0; i+3 <= len(needle); i++ {
		if !b.has(uint64(tri(needle, i))) {
			return false
		}
	}
	return true
}

func appendBloom(out []byte, b bloom) []byte {
	out = binary.BigEndian.AppendUint32(out, uint32(len(b)*8))
	for _, w := range b {
		out = binary.BigEndian.AppendUint64(out, w)
	}
	return out
}

// parseBlooms reads n length-prefixed blooms from data, which must be consumed exactly.
func parseBlooms(data []byte, n int) ([]bloom, bool) {
	out := make([]bloom, n)
	for i := 0; i < n; i++ {
		if len(data) < 4 {
			return nil, false
		}
		l := int(binary.BigEndian.Uint32(data))
		data = data[4:]
		if l%8 != 0 || l > len(data) || l > bloomMaxBits/8 {
			return nil, false
		}
		if l > 0 {
			b := make(bloom, l/8)
			for j := range b {
				b[j] = binary.BigEndian.Uint64(data[j*8:])
			}
			out[i] = b
		}
		data = data[l:]
	}
	return out, len(data) == 0
}
