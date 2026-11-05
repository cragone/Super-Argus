package query

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/stretchr/testify/require"
)

// TestLargeDataset builds ARGUS_QUERY_LINES lines (e.g. 10000000) directly as
// chunk files and times selector + line-filter queries over a 1h window.
// Skipped unless ARGUS_QUERY_LINES is set.
func TestLargeDataset(t *testing.T) {
	total, _ := strconv.Atoi(os.Getenv("ARGUS_QUERY_LINES"))
	if total <= 0 {
		t.Skip("set ARGUS_QUERY_LINES to run")
	}
	f := newFixture(t)
	const services, pods, linesPerChunk = 10, 10, 10000
	streams := services * pods
	perStream := total / streams
	hour := int64(time.Hour)
	t0 := base.UnixNano()

	type job struct {
		id    uint64
		start int
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var rows []models.Chunk
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				n := min(linesPerChunk, perStream-j.start)
				es := make([]chunk.Entry, n)
				for i := range es {
					k := j.start + i
					line := fmt.Sprintf(`level=info method=GET path=/api/v1/items/%d status=200 dur=%dms user_agent="Mozilla/5.0 (X11; Linux x86_64)" msg="request handled"`, k%977, k%250)
					if k%500 == 0 {
						line += " timeout"
					}
					es[i] = chunk.Entry{TS: t0 + int64(k)*hour/int64(perStream), Line: line}
				}
				data, meta, err := chunk.Encode(j.id, es)
				if err != nil {
					panic(err)
				}
				mu.Lock()
				id := fmt.Sprintf("big-%d-%d", j.id%100000, j.start)
				mu.Unlock()
				key := chunk.Key(meta.MinTS, j.id, id)
				if err := f.store.Put(key, data); err != nil {
					panic(err)
				}
				mu.Lock()
				rows = append(rows, models.Chunk{ID: id, StreamID: j.id, Path: key, MinTS: meta.MinTS, MaxTS: meta.MaxTS,
					LineCount: meta.Lines, BytesCompressed: int64(meta.BytesCompressed), BytesRaw: int64(meta.BytesRaw)})
				mu.Unlock()
			}
		}()
	}
	genStart := time.Now()
	for s := 0; s < streams; s++ {
		id := f.stream(fmt.Sprintf("svc%d", s%services), fmt.Sprintf("pod%d", s/services))
		for start := 0; start < perStream; start += linesPerChunk {
			jobs <- job{id, start}
		}
	}
	close(jobs)
	wg.Wait()
	require.NoError(t, f.g.CreateInBatches(rows, 500).Error)
	t.Logf("generated %d lines in %d chunks in %s", total, len(rows), time.Since(genStart).Round(time.Millisecond))

	f.svc.Limits.MaxRange = 0
	f.svc.Limits.MaxBytesScanned = 0
	f.svc.Limits.Timeout = time.Minute
	end := base.Add(time.Hour).UnixNano()
	for _, q := range []string{
		`{service="svc3"} |= "timeout"`,
		`{service=~"svc.*"} |= "timeout"`,
		`{service=~"svc.*"} |= "no-such-text"`,
		"{service=~\"svc.*\"} |~ `status=(4|5)\\d\\d`",
	} {
		for run := 0; run < 2; run++ {
			s := time.Now()
			res, err := f.svc.Range(t.Context(), "u", Request{Query: q, Start: t0, End: end, Limit: 1000, Backward: true})
			require.NoError(t, err)
			t.Logf("%-45s run%d: %v  entries=%d scanned=%d lines (%d MB) chunks=%d skipped=%d", q, run, time.Since(s).Round(time.Millisecond),
				len(res.Entries), res.Stats.LinesScanned, res.Stats.BytesScanned>>20, res.Stats.ChunksScanned, res.Stats.ChunksSkipped)
		}
	}
}
