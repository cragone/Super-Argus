// Package index maintains the stream registry and the chunk index in the metadata database.
package index

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cragone/Super-Argus/internal/models"
	"gorm.io/gorm"
)

// ErrStreamLimit is returned when an API key would exceed its active stream limit.
var ErrStreamLimit = errors.New("active stream limit reached for this API key: labels must be low-cardinality " +
	"(never use user IDs, request IDs or session IDs as labels)")

// LabelsText returns the normalized, sorted `k="v",...` form of labels.
func LabelsText(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(strconv.Quote(labels[k]))
	}
	return b.String()
}

// StreamID is the 63-bit FNV-1a hash of the normalized label text
// (63 bits so it fits a signed BIGINT).
func StreamID(labelsText string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(labelsText))
	return h.Sum64() & (1<<63 - 1)
}

type cached struct {
	id     uint64
	apiKey string
}

// Index is the metadata-store facade for streams and chunks.
type Index struct {
	db  *gorm.DB
	Now func() time.Time

	mu       sync.Mutex
	streams  map[string]cached // labels text -> stream
	keyCount map[string]int    // api key -> streams it created
	seen     map[uint64]time.Time
}

// New returns an Index over db.
func New(db *gorm.DB) *Index {
	return &Index{db: db, Now: time.Now, streams: map[string]cached{}, keyCount: map[string]int{}, seen: map[uint64]time.Time{}}
}

// Resolve returns the stream id for labels, creating the stream (and its label
// rows) on first sight. Creating a stream counts against apiKeyID's maxStreams
// (<= 0 means unlimited).
func (ix *Index) Resolve(apiKeyID string, labels map[string]string, maxStreams int) (uint64, error) {
	text := LabelsText(labels)
	now := ix.Now()
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if c, ok := ix.streams[text]; ok {
		ix.seen[c.id] = now
		return c.id, nil
	}
	id := StreamID(text)

	var found []models.Stream
	if err := ix.db.Where("id = ?", id).Limit(1).Find(&found).Error; err != nil {
		return 0, err
	}
	if len(found) == 1 {
		if found[0].LabelsText != text {
			return 0, fmt.Errorf("index: stream id collision for %s", text)
		}
		ix.streams[text] = cached{id, found[0].APIKeyID}
		ix.seen[id] = now
		return id, nil
	}

	if maxStreams > 0 {
		n, ok := ix.keyCount[apiKeyID]
		if !ok {
			var c int64
			if err := ix.db.Model(&models.Stream{}).Where("api_key_id = ?", apiKeyID).Count(&c).Error; err != nil {
				return 0, err
			}
			n = int(c)
		}
		if n >= maxStreams {
			return 0, ErrStreamLimit
		}
		ix.keyCount[apiKeyID] = n
	}

	lj, err := json.Marshal(labels)
	if err != nil {
		return 0, err
	}
	err = ix.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&models.Stream{ID: id, Labels: string(lj), LabelsText: text, APIKeyID: apiKeyID,
			FirstSeen: now, LastSeen: now}).Error; err != nil {
			return err
		}
		rows := make([]models.StreamLabel, 0, len(labels))
		for k, v := range labels {
			rows = append(rows, models.StreamLabel{StreamID: id, Key: k, Value: v})
		}
		return tx.Create(&rows).Error
	})
	if err != nil {
		return 0, err
	}
	if _, ok := ix.keyCount[apiKeyID]; ok {
		ix.keyCount[apiKeyID]++
	}
	ix.streams[text] = cached{id, apiKeyID}
	ix.seen[id] = now
	return id, nil
}

// FlushSeen persists last_seen for streams touched since the previous call.
func (ix *Index) FlushSeen() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if len(ix.seen) == 0 {
		return nil
	}
	err := ix.db.Transaction(func(tx *gorm.DB) error {
		for id, ts := range ix.seen {
			if err := tx.Model(&models.Stream{}).Where("id = ?", id).
				Updates(map[string]any{"last_seen": ts, "updated_at": ts}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		clear(ix.seen)
	}
	return err
}

// AddChunk records a flushed chunk.
func (ix *Index) AddChunk(c *models.Chunk) error { return ix.db.Create(c).Error }

// ExpiredChunks returns up to limit chunks whose newest line is older than cutoffNs.
func (ix *Index) ExpiredChunks(cutoffNs int64, limit int) ([]models.Chunk, error) {
	var cs []models.Chunk
	err := ix.db.Where("max_ts < ?", cutoffNs).Order("max_ts").Limit(limit).Find(&cs).Error
	return cs, err
}

// DeleteChunk removes a chunk's index row.
func (ix *Index) DeleteChunk(id string) error {
	return ix.db.Delete(&models.Chunk{}, "id = ?", id).Error
}

// PruneStreams deletes streams last seen before cutoff that have no chunks.
// It returns the number removed.
func (ix *Index) PruneStreams(cutoff time.Time) (int, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var ids []uint64
	err := ix.db.Model(&models.Stream{}).
		Where("last_seen < ? AND NOT EXISTS (SELECT 1 FROM chunks WHERE chunks.stream_id = streams.id)", cutoff).
		Pluck("id", &ids).Error
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	drop := ids[:0]
	for _, id := range ids {
		if _, recent := ix.seen[id]; !recent {
			drop = append(drop, id)
		}
	}
	if len(drop) == 0 {
		return 0, nil
	}
	if err := ix.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("stream_id IN ?", drop).Delete(&models.StreamLabel{}).Error; err != nil {
			return err
		}
		return tx.Where("id IN ?", drop).Delete(&models.Stream{}).Error
	}); err != nil {
		return 0, err
	}
	gone := make(map[uint64]bool, len(drop))
	for _, id := range drop {
		gone[id] = true
	}
	for text, c := range ix.streams {
		if gone[c.id] {
			delete(ix.streams, text)
			if n, ok := ix.keyCount[c.apiKey]; ok && n > 0 {
				ix.keyCount[c.apiKey] = n - 1
			}
		}
	}
	return len(drop), nil
}
