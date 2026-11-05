package retention

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/db"
	"github.com/cragone/Super-Argus/internal/index"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/stretchr/testify/require"
)

func TestOnceDeletesExpiredChunksFilesAndStreams(t *testing.T) {
	dir := t.TempDir()
	g, err := db.Open("sqlite:" + filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	sqlDB, _ := g.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Migrate(g))

	now := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	ix := index.New(g)
	ix.Now = func() time.Time { return now.Add(-20 * 24 * time.Hour) } // streams last seen long ago
	store := chunk.NewLocalStore(filepath.Join(dir, "chunks"))

	mk := func(svc string, age time.Duration) (uint64, models.Chunk) {
		id, err := ix.Resolve("k", map[string]string{"service": svc}, 0)
		require.NoError(t, err)
		ts := now.Add(-age)
		data, meta, err := chunk.Encode(id, []chunk.Entry{{TS: ts.UnixNano(), Line: "x"}})
		require.NoError(t, err)
		c := models.Chunk{ID: svc, StreamID: id, Path: chunk.Key(meta.MinTS, id, svc), MinTS: meta.MinTS, MaxTS: meta.MaxTS, LineCount: 1}
		require.NoError(t, store.Put(c.Path, data))
		require.NoError(t, ix.AddChunk(&c))
		return id, c
	}
	_, old := mk("old", 15*24*time.Hour)
	_, fresh := mk("fresh", 2*24*time.Hour)
	require.NoError(t, ix.FlushSeen())

	j := &Janitor{Index: ix, Store: store, Days: 14, Now: func() time.Time { return now }}
	chunks, streams, err := j.Once()
	require.NoError(t, err)
	require.Equal(t, 1, chunks)
	require.Equal(t, 1, streams, "stream with no chunks left is pruned")

	_, err = store.Get(old.Path)
	require.Error(t, err)
	_, err = store.Get(fresh.Path)
	require.NoError(t, err)
	var left []models.Chunk
	require.NoError(t, g.Find(&left).Error)
	require.Len(t, left, 1)
	require.Equal(t, "fresh", left[0].ID)

	// idempotent
	chunks, streams, err = j.Once()
	require.NoError(t, err)
	require.Zero(t, chunks+streams)

	// disabled
	j.Days = 0
	j.Now = func() time.Time { return now.Add(1000 * 24 * time.Hour) }
	chunks, _, err = j.Once()
	require.NoError(t, err)
	require.Zero(t, chunks)
}
