package chunk

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func bloomEntries() []Entry {
	var es []Entry
	for i := 0; i < 600; i++ {
		line := fmt.Sprintf("GET /v1/items/%d status=200 took=%dms", i, i%50)
		if i == 450 {
			line = "payment declined for order zebra-crossing-77"
		}
		es = append(es, Entry{TS: int64(i + 1), Line: line + strings.Repeat(" pad", 20)})
	}
	return es
}

func withSmallBlocks(t *testing.T) {
	old := TargetBlockBytes
	TargetBlockBytes = 4 << 10
	t.Cleanup(func() { TargetBlockBytes = old })
}

func scanAll(t *testing.T, r *Reader, needles []string) (lines int, hits int) {
	require.NoError(t, r.ScanContaining(0, 1<<40, needles, func(e Entry) bool {
		lines++
		if len(needles) > 0 && strings.Contains(e.Line, needles[0]) {
			hits++
		}
		return true
	}))
	return
}

func TestBloomSkipsBlocksWithoutFalseNegatives(t *testing.T) {
	withSmallBlocks(t)
	data, _, err := Encode(1, bloomEntries())
	require.NoError(t, err)
	r, err := NewReader(data)
	require.NoError(t, err)
	require.Greater(t, len(r.Blocks), 5)
	require.Len(t, r.blooms, len(r.Blocks))

	total, _ := scanAll(t, r, nil)
	require.Equal(t, 600, total)

	scanned, hits := scanAll(t, r, []string{"zebra-crossing"})
	require.Equal(t, 1, hits, "the matching line is always found")
	require.Less(t, scanned, 200, "most blocks are skipped")

	scanned, hits = scanAll(t, r, []string{"no-such-needle-anywhere"})
	require.Zero(t, hits)
	require.Less(t, scanned, 200)

	// Every line must pass its own substring: no false negatives.
	for _, e := range bloomEntries()[:100] {
		needle := e.Line[5:25]
		found := false
		require.NoError(t, r.ScanContaining(0, 1<<40, []string{needle}, func(x Entry) bool {
			found = found || x.Line == e.Line
			return true
		}))
		require.True(t, found, needle)
	}

	// Needles shorter than a trigram cannot be answered, so nothing is skipped.
	scanned, _ = scanAll(t, r, []string{"zz"})
	require.Equal(t, 600, scanned)
}

func TestBloomSizeIsBounded(t *testing.T) {
	data, _, err := Encode(1, bloomEntries())
	require.NoError(t, err)
	plain := len(data)
	r, err := NewReader(data)
	require.NoError(t, err)
	var n int
	for _, b := range r.blooms {
		n += len(b) * 8
		require.LessOrEqual(t, len(b)*64, bloomMaxBits)
	}
	require.Less(t, n*3, plain, "blooms stay a minor share of the chunk")
}

func TestVersion1ChunksStillRead(t *testing.T) {
	withSmallBlocks(t)
	data, _, err := Encode(1, bloomEntries())
	require.NoError(t, err)
	r, err := NewReader(data)
	require.NoError(t, err)

	// Rebuild the image as version 1: drop the bloom section, keep header, blocks, index and trailer.
	trailer := data[len(data)-trailerSize:]
	idxEnd := len(data) - trailerSize
	for _, b := range r.blooms {
		idxEnd -= 4 + len(b)*8
	}
	v1 := append([]byte{}, data[:idxEnd]...)
	v1 = append(v1, trailer...)
	v1[4] = versionNoBloom
	r1, err := NewReader(v1)
	require.NoError(t, err)
	require.Nil(t, r1.blooms)
	total, _ := scanAll(t, r1, nil)
	require.Equal(t, 600, total)
	scanned, hits := scanAll(t, r1, []string{"zebra-crossing"})
	require.Equal(t, 1, hits)
	require.Equal(t, 600, scanned, "no bloom, so every block is read")
}

func TestCorruptBloomSectionRejected(t *testing.T) {
	data, _, err := Encode(1, bloomEntries()[:20])
	require.NoError(t, err)
	bad := append([]byte{}, data...)
	bad = append(bad[:len(bad)-trailerSize-1], bad[len(bad)-trailerSize:]...) // lose a byte of the bloom section
	_, err = NewReader(bad)
	require.ErrorIs(t, err, ErrCorrupt)
}
