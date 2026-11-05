package wal

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func collect(t *testing.T, w *WAL) ([]Record, ReplayStats) {
	t.Helper()
	var got []Record
	st, err := w.Replay(func(_ uint64, r Record) { got = append(got, r) })
	require.NoError(t, err)
	return got, st
}

func TestAppendReplay(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, 0) // strict
	require.NoError(t, err)
	want := []Record{{1, 10, "a"}, {2, 20, "b"}, {1, 30, ""}}
	seg, err := w.Append(want)
	require.NoError(t, err)
	require.Equal(t, uint64(1), seg)
	require.NoError(t, w.Close())

	w2, err := Open(dir, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w2.Close() })
	got, st := collect(t, w2)
	require.Equal(t, want, got)
	require.Equal(t, ReplayStats{Records: 3}, st)
	require.Equal(t, uint64(2), w2.CurrentSeq(), "new process writes to a fresh segment")
}

func TestTornTailStopsReplay(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, 0)
	require.NoError(t, err)
	_, err = w.Append([]Record{{1, 1, "one"}, {1, 2, "two"}})
	require.NoError(t, err)
	require.NoError(t, w.Close())

	p := segName(dir, 1)
	b, _ := os.ReadFile(p)
	require.NoError(t, os.WriteFile(p, b[:len(b)-3], 0o644)) // chop the last record

	w2, err := Open(dir, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w2.Close() })
	got, st := collect(t, w2)
	require.Equal(t, []Record{{1, 1, "one"}}, got)
	require.Equal(t, 1, st.Torn)

	// bit flip is caught by the checksum
	b, _ = os.ReadFile(p)
	b[10] ^= 0xff // inside the first record's payload
	require.NoError(t, os.WriteFile(p, b, 0o644))
	got, st = collect(t, w2)
	require.Empty(t, got)
	require.Equal(t, 1, st.Torn)
}

func TestRotateAndTruncate(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	w.SegmentSize = 100

	var segs []uint64
	for i := 0; i < 5; i++ {
		s, err := w.Append([]Record{{1, int64(i), fmt.Sprintf("line-%040d", i)}})
		require.NoError(t, err)
		segs = append(segs, s)
	}
	require.NotEqual(t, segs[0], segs[4], "rotated")
	files, _ := listSegments(dir)
	require.GreaterOrEqual(t, len(files), 3)

	cur := w.CurrentSeq()
	require.NoError(t, w.TruncateBefore(cur))
	files, _ = listSegments(dir)
	require.Equal(t, []uint64{cur}, files, "only the active segment remains")

	// Rotating a non-empty segment advances; rotating an empty one is a no-op.
	s, err := w.Rotate()
	require.NoError(t, err)
	require.Equal(t, cur+1, s)
	s, err = w.Rotate()
	require.NoError(t, err)
	require.Equal(t, cur+1, s)
}

func TestAppendAfterClose(t *testing.T) {
	w, err := Open(t.TempDir(), time.Hour)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, w.Close())
	_, err = w.Append([]Record{{1, 1, "x"}})
	require.Error(t, err)
}
