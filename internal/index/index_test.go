package index

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/db"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newIndex(t *testing.T) (*Index, *gorm.DB) {
	t.Helper()
	g, err := db.Open("sqlite:" + filepath.Join(t.TempDir(), "t.db"))
	require.NoError(t, err)
	sqlDB, _ := g.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Migrate(g))
	return New(g), g
}

func TestLabelsTextAndID(t *testing.T) {
	a := LabelsText(map[string]string{"b": "2", "a": `x"y`})
	require.Equal(t, `a="x\"y",b="2"`, a)
	require.Equal(t, StreamID(a), StreamID(LabelsText(map[string]string{"a": `x"y`, "b": "2"})))
	require.Less(t, StreamID(a), uint64(1)<<63)
}

func TestResolveCreatesOnceAndPersists(t *testing.T) {
	ix, g := newIndex(t)
	labels := map[string]string{"service": "api", "env": "prod"}
	id1, err := ix.Resolve("k1", labels, 10)
	require.NoError(t, err)
	id2, err := ix.Resolve("k1", labels, 10)
	require.NoError(t, err)
	require.Equal(t, id1, id2)

	var n int64
	g.Model(&models.Stream{}).Count(&n)
	require.Equal(t, int64(1), n)
	g.Model(&models.StreamLabel{}).Where("stream_id = ?", id1).Count(&n)
	require.Equal(t, int64(2), n)

	// a fresh Index (restart) finds the existing row instead of inserting
	id3, err := New(g).Resolve("k1", labels, 10)
	require.NoError(t, err)
	require.Equal(t, id1, id3)
}

func TestStreamLimitPerKey(t *testing.T) {
	ix, _ := newIndex(t)
	for _, v := range []string{"a", "b"} {
		_, err := ix.Resolve("k1", map[string]string{"s": v}, 2)
		require.NoError(t, err)
	}
	_, err := ix.Resolve("k1", map[string]string{"s": "c"}, 2)
	require.ErrorIs(t, err, ErrStreamLimit)
	require.Contains(t, err.Error(), "request IDs")
	// existing streams still resolve; other keys have their own budget
	_, err = ix.Resolve("k1", map[string]string{"s": "a"}, 2)
	require.NoError(t, err)
	_, err = ix.Resolve("k2", map[string]string{"s": "c"}, 2)
	require.NoError(t, err)
}

func TestPruneAndChunks(t *testing.T) {
	ix, g := newIndex(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ix.Now = func() time.Time { return base }
	idOld, _ := ix.Resolve("k", map[string]string{"s": "old"}, 0)
	idKeep, _ := ix.Resolve("k", map[string]string{"s": "keep"}, 0)
	idIdle, _ := ix.Resolve("k", map[string]string{"s": "idle"}, 0)
	require.NoError(t, ix.FlushSeen())
	require.NoError(t, ix.AddChunk(&models.Chunk{ID: "c1", StreamID: idKeep, Path: "p", MinTS: 1, MaxTS: 5, LineCount: 1}))
	require.NoError(t, ix.AddChunk(&models.Chunk{ID: "c2", StreamID: idOld, Path: "p2", MinTS: 1, MaxTS: 9, LineCount: 1}))

	cs, err := ix.ExpiredChunks(6, 10)
	require.NoError(t, err)
	require.Len(t, cs, 1)
	require.Equal(t, "c1", cs[0].ID)
	require.NoError(t, ix.DeleteChunk("c1"))

	// idOld has a chunk; idKeep and idIdle have none and are old
	n, err := ix.PruneStreams(base.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, n)
	var left []models.Stream
	g.Find(&left)
	require.Len(t, left, 1)
	require.Equal(t, idOld, left[0].ID)
	var labels int64
	g.Model(&models.StreamLabel{}).Count(&labels)
	require.Equal(t, int64(1), labels, "labels cascade with pruned streams")
	_ = idIdle

	// pruned streams are recreated on demand
	_, err = ix.Resolve("k", map[string]string{"s": "idle"}, 0)
	require.NoError(t, err)
}
