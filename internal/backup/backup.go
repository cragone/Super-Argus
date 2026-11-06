// Package backup takes a consistent copy of an Argus data directory while the
// server keeps running.
//
// The order is what makes a live copy safe:
//
//  1. WAL segments are copied first. They hold every line not yet in a chunk.
//  2. The SQLite index is snapshotted with VACUUM INTO (a transactionally
//     consistent copy that does not block writers for long).
//  3. Chunk files are copied last. They are immutable, so already-copied files
//     are skipped on later runs and the copy is incremental.
//
// A line flushed between steps 1 and 2 is then in both the WAL copy and the
// index, so restoring replays it a second time (at-least-once, a duplicate).
// Copying in any other order can lose it. Chunks written after the snapshot
// are simply unreferenced files.
package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cragone/Super-Argus/internal/db"
	"gorm.io/gorm"
)

// Result summarises a backup run.
type Result struct {
	DBBytes       int64
	WALFiles      int
	ChunksCopied  int
	ChunksSkipped int // already present with the same size
	// MissingChunks are indexed chunks absent from the copy, usually deleted by
	// retention between the snapshot and the copy. They are not errors.
	MissingChunks int
}

// Run backs up dataDir into dest. g must be the live database; only SQLite
// can be snapshotted this way (use pg_dump for Postgres).
func Run(ctx context.Context, g *gorm.DB, dataDir, dest string) (Result, error) {
	var res Result
	if g.Dialector.Name() != "sqlite" {
		return res, errors.New("backup supports SQLite only; back up Postgres with pg_dump")
	}
	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return res, err
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return res, err
	}
	if absDest == absData || strings.HasPrefix(absDest+string(filepath.Separator), absData+string(filepath.Separator)) {
		return res, errors.New("destination must be outside the data directory")
	}
	if err := os.MkdirAll(absDest, 0o755); err != nil {
		return res, err
	}

	// 1. WAL: replace the previous copy wholesale, old segments are obsolete.
	walDest := filepath.Join(absDest, "wal")
	if err := os.RemoveAll(walDest); err != nil {
		return res, err
	}
	if res.WALFiles, _, err = copyTree(ctx, filepath.Join(absData, "wal"), walDest, false); err != nil {
		return res, fmt.Errorf("copy wal: %w", err)
	}

	// 2. Index snapshot, written beside the target and renamed into place so a
	// failed run never leaves a half-written argus.db.
	dbDest := filepath.Join(absDest, "argus.db")
	tmp := dbDest + ".tmp"
	_ = os.Remove(tmp)
	if err := g.WithContext(ctx).Exec("VACUUM INTO ?", filepath.ToSlash(tmp)).Error; err != nil {
		return res, fmt.Errorf("snapshot database: %w", err)
	}
	if err := os.Rename(tmp, dbDest); err != nil {
		return res, err
	}
	fi, err := os.Stat(dbDest)
	if err != nil {
		return res, err
	}
	res.DBBytes = fi.Size()

	// 3. Chunks, incrementally.
	chunksDest := filepath.Join(absDest, "chunks")
	if res.ChunksCopied, res.ChunksSkipped, err = copyTree(ctx, filepath.Join(absData, "chunks"), chunksDest, true); err != nil {
		return res, fmt.Errorf("copy chunks: %w", err)
	}

	// Verify the snapshot we wrote, not the live database.
	snap, err := db.Open("sqlite:" + dbDest)
	if err != nil {
		return res, fmt.Errorf("open snapshot: %w", err)
	}
	sqlDB, err := snap.DB()
	if err != nil {
		return res, err
	}
	defer sqlDB.Close()
	var check string
	if err := snap.Raw("PRAGMA integrity_check").Scan(&check).Error; err != nil || check != "ok" {
		return res, fmt.Errorf("snapshot failed integrity check: %v %s", err, check)
	}
	var paths []string
	if err := snap.Raw("SELECT path FROM chunks").Scan(&paths).Error; err != nil {
		return res, fmt.Errorf("list chunks: %w", err)
	}
	for _, p := range paths {
		if _, err := os.Stat(filepath.Join(chunksDest, filepath.FromSlash(p))); err != nil {
			res.MissingChunks++
		}
	}
	return res, nil
}

// copyTree copies the files under src into dst. With skipSame, a destination
// file of equal size is left alone (chunks are immutable). A missing src is an
// empty tree. Files that vanish mid-copy (retention, WAL truncation) are skipped.
func copyTree(ctx context.Context, src, dst string, skipSame bool) (copied, skipped int, err error) {
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return 0, 0, nil
	}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if errors.Is(werr, fs.ErrNotExist) {
				return nil
			}
			return werr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if strings.HasSuffix(p, ".tmp") {
			return nil // a chunk mid-write; it is not indexed yet
		}
		in, err := os.Open(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer in.Close()
		fi, err := in.Stat()
		if err != nil {
			return err
		}
		if skipSame {
			if ti, err := os.Stat(target); err == nil && ti.Size() == fi.Size() {
				skipped++
				return nil
			}
		}
		if err := writeFile(target, in); err != nil {
			return err
		}
		copied++
		return nil
	})
	return copied, skipped, err
}

// writeFile copies r to path via a temp file and rename.
func writeFile(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
