// Package retention deletes expired chunk files and index rows.
package retention

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/index"
)

// Janitor enforces the retention window.
type Janitor struct {
	Index *index.Index
	Store chunk.Store
	Days  int
	Now   func() time.Time
	Log   *slog.Logger
}

const batchSize = 500

// Once deletes chunks whose newest line is older than the window, then prunes
// streams that have no chunks and have not been seen since. It returns the
// number of chunks and streams removed. Days <= 0 disables retention.
func (j *Janitor) Once() (chunks, streams int, err error) {
	if j.Days <= 0 {
		return 0, 0, nil
	}
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	cutoff := now().Add(-time.Duration(j.Days) * 24 * time.Hour)
	var errs []error
	for {
		cs, err := j.Index.ExpiredChunks(cutoff.UnixNano(), batchSize)
		if err != nil {
			errs = append(errs, err)
			break
		}
		deleted := 0
		for _, c := range cs {
			// File first: a crash leaves an index row pointing at nothing (harmless, retried),
			// never an unindexed file that would leak disk.
			if err := j.Store.Delete(c.Path); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := j.Index.DeleteChunk(c.ID); err != nil {
				errs = append(errs, err)
				continue
			}
			deleted++
		}
		chunks += deleted
		if len(cs) < batchSize || deleted == 0 {
			break
		}
	}
	n, err := j.Index.PruneStreams(cutoff)
	if err != nil {
		errs = append(errs, err)
	}
	return chunks, n, errors.Join(errs...)
}

// Run calls Once every interval until ctx is cancelled.
func (j *Janitor) Run(ctx context.Context, every time.Duration) {
	log := j.Log
	if log == nil {
		log = slog.Default()
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		c, s, err := j.Once()
		if err != nil {
			log.Error("retention", "err", err)
		} else if c > 0 || s > 0 {
			log.Info("retention", "chunks_deleted", c, "streams_pruned", s)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
