package ingest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/query"
	"github.com/stretchr/testify/require"
)

func (r *rig) queryAll(svc *query.Service) []query.Entry {
	r.t.Helper()
	res, err := svc.Range(context.Background(), "u", query.Request{Query: `{service=~"s.*"}`,
		Start: t0.Add(-time.Hour).UnixNano(), End: t0.Add(4 * time.Hour).UnixNano(), Limit: 5000})
	require.NoError(r.t, err)
	return res.Entries
}

func TestQueryAcrossHeadsAndChunks(t *testing.T) {
	r := newRig(t)
	svc := query.NewService(r.g, r.store, r.eng, query.DefaultLimits())

	var body strings.Builder
	for i := 0; i < 100; i++ {
		body.WriteString(line(r.now.Add(time.Duration(i)*time.Millisecond), fmt.Sprintf("s%d", i%4), fmt.Sprintf("msg %d", i)))
	}
	require.Equal(t, 200, r.push(body.String(), nil).Code)
	require.Len(t, r.queryAll(svc), 100, "all lines still in heads")

	r.now = r.now.Add(2 * time.Hour)
	require.NoError(t, r.eng.Tick()) // flush
	require.Zero(t, r.eng.HeadLines())
	require.Len(t, r.queryAll(svc), 100, "all lines now in chunks")

	require.Equal(t, 200, r.push(line(r.now, "s0", "fresh"), nil).Code)
	es := r.queryAll(svc)
	require.Len(t, es, 101, "chunks + head")
	require.Equal(t, "fresh", es[len(es)-1].Line)
}

// Queries racing with flushes must never lose or duplicate lines.
func TestQueryDuringFlushIsConsistent(t *testing.T) {
	r := newRig(t)
	svc := query.NewService(r.g, r.store, r.eng, query.DefaultLimits())
	const batches, perBatch = 30, 20

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // flusher
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if err := r.eng.FlushAll(); err != nil {
					t.Logf("flush error: %v", err)
				}
			}
		}
	}()

	for b := 0; b < batches; b++ {
		var body strings.Builder
		for i := 0; i < perBatch; i++ {
			body.WriteString(line(r.now.Add(time.Duration(b*perBatch+i)*time.Millisecond), fmt.Sprintf("s%d", i%3), fmt.Sprintf("m-%d-%d", b, i)))
		}
		require.Equal(t, 200, r.push(body.String(), nil).Code)
		es := r.queryAll(svc)
		seen := map[string]int{}
		for _, e := range es {
			seen[e.Line]++
		}
		dups, want := 0, map[string]bool{}
		for l, n := range seen {
			if n > 1 {
				dups++
			}
			want[l] = true
		}
		missing := 0
		for bb := 0; bb <= b; bb++ {
			for i := 0; i < perBatch; i++ {
				if !want[fmt.Sprintf("m-%d-%d", bb, i)] {
					missing++
				}
			}
		}
		require.Zero(t, dups+missing, "batch %d: %d duplicated, %d missing (got %d lines)", b, dups, missing, len(es))
	}
	close(stop)
	wg.Wait()
}
