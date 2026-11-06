package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/db"
	"github.com/cragone/Super-Argus/internal/index"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// rig is a data directory with an index, some chunks and WAL segments.
type rig struct {
	t     *testing.T
	data  string
	g     *gorm.DB
	store *chunk.LocalStore
	ix    *index.Index
}

func newRig(t *testing.T) *rig {
	data := filepath.Join(t.TempDir(), "data")
	g, err := db.Open("sqlite:" + filepath.Join(data, "argus.db"))
	require.NoError(t, err)
	sqlDB, _ := g.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Migrate(g))
	require.NoError(t, os.MkdirAll(filepath.Join(data, "wal"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(data, "wal", "wal-000001.log"), []byte("pending lines"), 0o644))
	return &rig{t: t, data: data, g: g, store: chunk.NewLocalStore(filepath.Join(data, "chunks")), ix: index.New(g)}
}

func (r *rig) addChunk(id string, line string) string {
	sid, err := r.ix.Resolve("k", map[string]string{"service": "api"}, 0)
	require.NoError(r.t, err)
	data, meta, err := chunk.Encode(sid, []chunk.Entry{{TS: 1_700_000_000_000_000_000, Line: line}})
	require.NoError(r.t, err)
	key := chunk.Key(meta.MinTS, sid, id)
	require.NoError(r.t, r.store.Put(key, data))
	require.NoError(r.t, r.ix.AddChunk(&models.Chunk{ID: id, StreamID: sid, Path: key, MinTS: meta.MinTS, MaxTS: meta.MaxTS, LineCount: 1}))
	return key
}

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	r := newRig(t)
	k1 := r.addChunk("c1", "first")
	dest := filepath.Join(t.TempDir(), "bk")

	res, err := Run(context.Background(), r.g, r.data, dest)
	require.NoError(t, err)
	require.Equal(t, 1, res.ChunksCopied)
	require.Equal(t, 1, res.WALFiles)
	require.Zero(t, res.MissingChunks)
	require.Positive(t, res.DBBytes)

	// "Restore": the backup directory is a complete data directory.
	snap, err := db.Open("sqlite:" + filepath.Join(dest, "argus.db"))
	require.NoError(t, err)
	sqlDB, _ := snap.DB()
	defer sqlDB.Close()
	var cs []models.Chunk
	require.NoError(t, snap.Find(&cs).Error)
	require.Len(t, cs, 1)
	got, err := chunk.NewLocalStore(filepath.Join(dest, "chunks")).Get(k1)
	require.NoError(t, err)
	rd, err := chunk.NewReader(got)
	require.NoError(t, err)
	require.Equal(t, 1, rd.Meta.Lines)
	b, err := os.ReadFile(filepath.Join(dest, "wal", "wal-000001.log"))
	require.NoError(t, err)
	require.Equal(t, "pending lines", string(b))
}

func TestBackupIsIncrementalAndRefreshesWAL(t *testing.T) {
	r := newRig(t)
	r.addChunk("c1", "first")
	dest := filepath.Join(t.TempDir(), "bk")
	_, err := Run(context.Background(), r.g, r.data, dest)
	require.NoError(t, err)

	r.addChunk("c2", "second")
	require.NoError(t, os.Remove(filepath.Join(r.data, "wal", "wal-000001.log")))
	require.NoError(t, os.WriteFile(filepath.Join(r.data, "wal", "wal-000002.log"), []byte("new"), 0o644))

	res, err := Run(context.Background(), r.g, r.data, dest)
	require.NoError(t, err)
	require.Equal(t, 1, res.ChunksCopied, "only the new chunk is copied")
	require.Equal(t, 1, res.ChunksSkipped, "the unchanged chunk is not copied again")
	_, err = os.Stat(filepath.Join(dest, "wal", "wal-000001.log"))
	require.True(t, os.IsNotExist(err), "obsolete WAL segments do not linger in the copy")
	_, err = os.Stat(filepath.Join(dest, "wal", "wal-000002.log"))
	require.NoError(t, err)
}

func TestBackupReportsMissingChunks(t *testing.T) {
	r := newRig(t)
	k := r.addChunk("c1", "first")
	require.NoError(t, r.store.Delete(k)) // retention removed the file but the index row is still there
	res, err := Run(context.Background(), r.g, r.data, filepath.Join(t.TempDir(), "bk"))
	require.NoError(t, err)
	require.Equal(t, 1, res.MissingChunks, "reported, not fatal")
}

func TestBackupRefusesDestinationInsideData(t *testing.T) {
	r := newRig(t)
	for _, dest := range []string{r.data, filepath.Join(r.data, "backups")} {
		_, err := Run(context.Background(), r.g, r.data, dest)
		require.Error(t, err, dest)
	}
	_, err := os.Stat(filepath.Join(r.data, "backups"))
	require.True(t, os.IsNotExist(err), "nothing was created inside the data directory")
}

func TestBackupWithNoChunksOrWAL(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	g, err := db.Open("sqlite:" + filepath.Join(data, "argus.db"))
	require.NoError(t, err)
	sqlDB, _ := g.DB()
	defer sqlDB.Close()
	require.NoError(t, db.Migrate(g))
	res, err := Run(context.Background(), g, data, filepath.Join(t.TempDir(), "bk"))
	require.NoError(t, err)
	require.Zero(t, res.ChunksCopied+res.WALFiles)
}
