package chunk

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func mkEntries(n int) []Entry {
	es := make([]Entry, n)
	for i := range es {
		es[i] = Entry{TS: int64(1_000_000 + i*10), Line: fmt.Sprintf("line %d %s", i, strings.Repeat("x", i%50))}
	}
	return es
}

func TestRoundTripMultiBlock(t *testing.T) {
	old := TargetBlockBytes
	TargetBlockBytes = 1024
	t.Cleanup(func() { TargetBlockBytes = old })

	es := mkEntries(5000)
	rand.New(rand.NewSource(1)).Shuffle(len(es), func(i, j int) { es[i], es[j] = es[j], es[i] })
	data, meta, err := Encode(42, es)
	require.NoError(t, err)
	require.Equal(t, 5000, meta.Lines)
	require.Equal(t, len(data), meta.BytesCompressed)

	r, err := NewReader(data)
	require.NoError(t, err)
	require.Greater(t, len(r.Blocks), 5)
	require.Equal(t, uint64(42), r.Meta.StreamID)
	require.Equal(t, 5000, r.Meta.Lines)

	var got []Entry
	require.NoError(t, r.Scan(0, 1<<62, func(e Entry) bool { got = append(got, e); return true }))
	require.Equal(t, mkEntries(5000), got)
}

func TestScanRangeSkipsBlocks(t *testing.T) {
	old := TargetBlockBytes
	TargetBlockBytes = 1024
	t.Cleanup(func() { TargetBlockBytes = old })
	data, _, err := Encode(1, mkEntries(5000))
	require.NoError(t, err)
	r, err := NewReader(data)
	require.NoError(t, err)

	from, to := int64(1_000_000+1000*10), int64(1_000_000+1100*10)
	var n int
	require.NoError(t, r.Scan(from, to, func(e Entry) bool {
		require.GreaterOrEqual(t, e.TS, from)
		require.Less(t, e.TS, to)
		n++
		return true
	}))
	require.Equal(t, 100, n)

	n = 0
	require.NoError(t, r.Scan(0, 1<<62, func(Entry) bool { n++; return n < 3 }))
	require.Equal(t, 3, n)
}

func TestCorrupt(t *testing.T) {
	data, _, err := Encode(1, mkEntries(10))
	require.NoError(t, err)
	_, err = NewReader(data[:len(data)-1])
	require.ErrorIs(t, err, ErrCorrupt)
	_, err = NewReader([]byte("nope"))
	require.ErrorIs(t, err, ErrCorrupt)

	bad := append([]byte(nil), data...)
	bad[headerSize+2] ^= 0xff
	bad[headerSize+3] ^= 0xff
	r, err := NewReader(bad)
	require.NoError(t, err)
	require.Error(t, r.Scan(0, 1<<62, func(Entry) bool { return true }))

	_, _, err = Encode(1, nil)
	require.Error(t, err)
}

func TestLocalStore(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStore(dir)
	key := Key(1_700_000_000_000_000_000, 7, "abc")
	require.Equal(t, "2023/11/14/7/abc.chk", key)
	require.NoError(t, s.Put(key, []byte("hello")))
	b, err := s.Get(key)
	require.NoError(t, err)
	require.Equal(t, "hello", string(b))

	require.NoError(t, s.Delete(key))
	require.NoError(t, s.Delete(key)) // idempotent
	_, err = s.Get(key)
	require.ErrorIs(t, err, os.ErrNotExist)
	entries, _ := os.ReadDir(dir)
	require.Empty(t, entries, "empty parent dirs removed")

	for _, bad := range []string{"", "../x", "a/../../x", "/abs", "a//b", `a\b`} {
		require.Error(t, s.Put(bad, nil), bad)
	}
}
