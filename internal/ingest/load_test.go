package ingest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/index"
	"github.com/cragone/Super-Argus/internal/wal"
	"github.com/stretchr/testify/require"
)

// TestLoad pushes ARGUS_LOAD_RATE lines/sec (default 2000) for ARGUS_LOAD_SECONDS
// seconds through the real write path (200ms WAL sync, real clock, flush loop),
// then shuts down and checks every line is in a chunk. Skipped unless
// ARGUS_LOAD_SECONDS is set, e.g. ARGUS_LOAD_SECONDS=600 go test ./internal/ingest -run TestLoad -timeout 20m
func TestLoad(t *testing.T) {
	secs, _ := strconv.Atoi(os.Getenv("ARGUS_LOAD_SECONDS"))
	if secs <= 0 {
		t.Skip("set ARGUS_LOAD_SECONDS to run")
	}
	rate := 2000
	if v, _ := strconv.Atoi(os.Getenv("ARGUS_LOAD_RATE")); v > 0 {
		rate = v
	}
	r := newRig(t)
	require.NoError(t, r.eng.Close())
	w, err := wal.Open(filepath.Join(r.dir, "wal2"), 200*time.Millisecond)
	require.NoError(t, err)
	ix := index.New(r.g)
	cfg := DefaultEngineConfig()
	cfg.MaxHeadBytes = 256 << 10 // flush often so the run exercises chunk writes
	eng := NewEngine(w, ix, chunk.NewLocalStore(filepath.Join(r.dir, "chunks2")), cfg)
	require.NoError(t, eng.Recover())
	h := NewHandler(r.g, eng, ix, DefaultLimits())

	ctx, cancel := context.WithCancel(context.Background())
	var bg sync.WaitGroup
	bg.Add(1)
	go func() { defer bg.Done(); eng.Run(ctx) }()

	const tick = 100 * time.Millisecond
	perTick := rate * int(tick/time.Millisecond) / 1000
	sent := 0
	start := time.Now()
	for n := 0; time.Since(start) < time.Duration(secs)*time.Second; n++ {
		var b strings.Builder
		for i := 0; i < perTick; i++ {
			b.WriteString(line(time.Now(), fmt.Sprintf("svc%d", i%20), fmt.Sprintf(`level=info msg="request handled" n=%d i=%d path=/api/v1/things/%d`, n, i, i%97)))
		}
		req := newReq(r, b.String())
		rec := serve(h, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		sent += perTick
		time.Sleep(time.Until(start.Add(time.Duration(n+1) * tick)))
	}
	cancel()
	bg.Wait()
	require.NoError(t, eng.Close())

	rr := &rig{t: t, g: r.g, store: chunk.NewLocalStore(filepath.Join(r.dir, "chunks2"))}
	total := 0
	for _, n := range rr.storedLines() {
		total += n
	}
	// storedLines dedupes identical text; compare by chunk line counts instead
	var sum int64
	require.NoError(t, r.g.Raw("SELECT COALESCE(SUM(line_count),0) FROM chunks").Scan(&sum).Error)
	t.Logf("sent=%d stored=%d distinct=%d in %s", sent, sum, total, time.Since(start).Round(time.Second))
	require.Equal(t, int64(sent), sum, "no lines lost")
}

func newReq(r *rig, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/push", strings.NewReader(body))
	req.Header.Set(KeyHeader, r.apiKey)
	return req
}

func serve(h *Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.Push(rec, req)
	return rec
}
