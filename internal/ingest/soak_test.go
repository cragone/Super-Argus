package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/index"
	"github.com/cragone/Super-Argus/internal/query"
	"github.com/cragone/Super-Argus/internal/wal"
	"github.com/stretchr/testify/require"
)

// TestSoak drives the real write path with alternating base and 5x-burst load
// while queries run against it, and checks nothing is lost, pushes stay fast,
// and memory and goroutines stay flat. Skipped unless ARGUS_SOAK_SECONDS is set:
//
//	ARGUS_SOAK_SECONDS=300 ARGUS_SOAK_RATE=2000 go test ./internal/ingest -run TestSoak -timeout 30m -v
//
// ARGUS_SOAK_RATE is the base lines/sec; bursts run at 5x for half of each 10s cycle.
func TestSoak(t *testing.T) {
	secs, _ := strconv.Atoi(os.Getenv("ARGUS_SOAK_SECONDS"))
	if secs <= 0 {
		t.Skip("set ARGUS_SOAK_SECONDS to run")
	}
	base := 2000
	if v, _ := strconv.Atoi(os.Getenv("ARGUS_SOAK_RATE")); v > 0 {
		base = v
	}
	const burstFactor = 5

	r := newRig(t)
	require.NoError(t, r.eng.Close())
	w, err := wal.Open(filepath.Join(r.dir, "wal-soak"), 200*time.Millisecond)
	require.NoError(t, err)
	ix := index.New(r.g)
	store := chunk.NewLocalStore(filepath.Join(r.dir, "chunks-soak"))
	cfg := DefaultEngineConfig()
	cfg.MaxHeadBytes = 512 << 10
	eng := NewEngine(w, ix, store, cfg)
	require.NoError(t, eng.Recover())
	lim := DefaultLimits()
	lim.DefaultLinesPerSec = 1_000_000 // this test is about the write path, not the limiter
	lim.BytesPerSec = 1 << 30
	h := NewHandler(r.g, eng, ix, lim)
	svc := query.NewService(r.g, store, eng, query.DefaultLimits())

	ctx, cancel := context.WithCancel(context.Background())
	var bg sync.WaitGroup
	bg.Add(1)
	go func() { defer bg.Done(); eng.Run(ctx) }()

	// Query load: a filtered log query every 250ms and a metric query every second.
	var queries, queryErrs atomic.Int64
	var firstErr atomic.Value
	for _, q := range []struct {
		every time.Duration
		run   func() error
	}{
		{250 * time.Millisecond, func() error {
			_, err := svc.Range(ctx, "soak", query.Request{Query: `{service=~"svc1.*"} |= "request handled"`,
				Start: time.Now().Add(-10 * time.Minute).UnixNano(), End: time.Now().UnixNano(), Limit: 100, Backward: true})
			return err
		}},
		{time.Second, func() error {
			_, err := svc.Metric(ctx, "soak", query.MetricRequest{Query: `sum by (service) (count_over_time({service=~"svc.*"}[30s]))`,
				Start: time.Now().Add(-5 * time.Minute).UnixNano(), End: time.Now().UnixNano(), Step: 15 * time.Second})
			return err
		}},
	} {
		bg.Add(1)
		go func() {
			defer bg.Done()
			tk := time.NewTicker(q.every)
			defer tk.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tk.C:
					queries.Add(1)
					if err := q.run(); err != nil && ctx.Err() == nil {
						queryErrs.Add(1)
						firstErr.CompareAndSwap(nil, err.Error())
					}
				}
			}
		}()
	}

	// Memory and goroutine sampling every 2s, after a GC so we see live data.
	var heaps []float64
	startGoroutines := runtime.NumGoroutine()
	bg.Add(1)
	go func() {
		defer bg.Done()
		tk := time.NewTicker(2 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				runtime.GC()
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				heaps = append(heaps, float64(m.HeapAlloc))
				if path := os.Getenv("ARGUS_SOAK_HEAPPROFILE"); path != "" { // latest live-heap profile, for go tool pprof
					if f, err := os.Create(path); err == nil {
						_ = pprof.WriteHeapProfile(f)
						f.Close()
					}
				}
			}
		}
	}()

	const tick = 100 * time.Millisecond
	sent := 0
	var lat []time.Duration
	start := time.Now()
	for n := 0; time.Since(start) < time.Duration(secs)*time.Second; n++ {
		rate := base
		if (time.Since(start)/(5*time.Second))%2 == 1 {
			rate = base * burstFactor
		}
		perTick := rate / 10
		var b strings.Builder
		for i := 0; i < perTick; i++ {
			b.WriteString(line(time.Now(), fmt.Sprintf("svc%d", i%20),
				fmt.Sprintf(`level=info msg="request handled" n=%d i=%d path=/api/v1/things/%d user=u%d`, n, i, i%97, i%500)))
		}
		began := time.Now()
		rec := serve(h, newReq(r, b.String()))
		lat = append(lat, time.Since(began))
		require.Equal(t, 200, rec.Code, rec.Body.String())
		sent += perTick
		time.Sleep(time.Until(start.Add(time.Duration(n+1) * tick)))
	}
	cancel()
	bg.Wait()
	require.NoError(t, eng.Close())

	var stored int64
	require.NoError(t, r.g.Raw("SELECT COALESCE(SUM(line_count),0) FROM chunks").Scan(&stored).Error)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p99, maxLat := lat[len(lat)*99/100], lat[len(lat)-1]
	mean := func(xs []float64) float64 {
		s := 0.0
		for _, x := range xs {
			s += x
		}
		return s / float64(len(xs))
	}
	var growth string
	if q := len(heaps) / 4; q >= 2 {
		early, late := mean(heaps[q:2*q]), mean(heaps[len(heaps)-q:])
		growth = fmt.Sprintf("heap(2nd quarter)=%.1fMB heap(last quarter)=%.1fMB", early/1e6, late/1e6)
		require.LessOrEqual(t, late, early*1.5+32e6, "memory grew during the soak: "+growth)
	}
	t.Logf("duration=%s base=%d/s burst=%d/s sent=%d stored=%d", time.Since(start).Round(time.Second), base, base*burstFactor, sent, stored)
	t.Logf("push latency p99=%s max=%s; queries=%d errors=%d; %s; goroutines %d -> %d",
		p99.Round(time.Millisecond), maxLat.Round(time.Millisecond), queries.Load(), queryErrs.Load(), growth, startGoroutines, runtime.NumGoroutine())

	require.Equal(t, int64(sent), stored, "no lines lost")
	require.Zero(t, queryErrs.Load(), "queries failed under load: %v", firstErr.Load())
	require.Less(t, maxLat, 5*time.Second, "a push stalled")
	require.LessOrEqual(t, runtime.NumGoroutine(), startGoroutines+10, "goroutines leaked")
}
