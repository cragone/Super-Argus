package argus

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type received struct {
	key, encoding string
	entries       []entry
}

// fakeArgus records pushes. status lets a test script failures.
type fakeArgus struct {
	mu     sync.Mutex
	got    []received
	status func(call int) int
	calls  atomic.Int32
	block  chan struct{}
}

func (f *fakeArgus) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := int(f.calls.Add(1))
		if f.block != nil {
			<-f.block
		}
		code := http.StatusOK
		if f.status != nil {
			code = f.status(call)
		}
		if code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		zr, err := gzip.NewReader(r.Body)
		require.NoError(t, err)
		rec := received{key: r.Header.Get("X-Argus-Key"), encoding: r.Header.Get("Content-Encoding")}
		sc := bufio.NewScanner(zr)
		for sc.Scan() {
			var e entry
			require.NoError(t, json.Unmarshal(sc.Bytes(), &e))
			rec.entries = append(rec.entries, e)
		}
		f.mu.Lock()
		f.got = append(f.got, rec)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]int{"accepted": len(rec.entries), "rejected": 0})
	})
}

func (f *fakeArgus) lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.got {
		for _, e := range r.entries {
			out = append(out, e.Line)
		}
	}
	return out
}

func newShipper(t *testing.T, f *fakeArgus, mod func(*Config)) *Shipper {
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	cfg := Config{URL: srv.URL + "/", APIKey: "argus_test", Labels: map[string]string{"service": "testapp", "env": "test"},
		BatchSize: 3, FlushEvery: 20 * time.Millisecond, Backoff: time.Millisecond}
	if mod != nil {
		mod(&cfg)
	}
	s := New(cfg)
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}

func closeNow(s *Shipper) { s.Close(context.Background()) }

func TestShipsGzippedNDJSONWithKeyAndLabels(t *testing.T) {
	f := &fakeArgus{}
	s := newShipper(t, f, nil)
	now := time.Now()
	for _, l := range []string{"a", "b", "c", "d"} {
		s.Send(now, l)
	}
	closeNow(s)

	require.Equal(t, []string{"a", "b", "c", "d"}, f.lines())
	require.Equal(t, "argus_test", f.got[0].key)
	require.Equal(t, "gzip", f.got[0].encoding)
	require.Equal(t, map[string]string{"service": "testapp", "env": "test"}, f.got[0].entries[0].Labels)
	_, err := time.Parse(time.RFC3339Nano, f.got[0].entries[0].TS)
	require.NoError(t, err)
	require.Len(t, f.got[0].entries, 3, "batch size is respected")
	require.Equal(t, Stats{Sent: 4, Dropped: 0, Batches: 2}, s.Stats())
}

func TestFlushesPartialBatchOnTimer(t *testing.T) {
	f := &fakeArgus{}
	s := newShipper(t, f, nil)
	s.Send(time.Now(), "lonely")
	require.Eventually(t, func() bool { return len(f.lines()) == 1 }, 2*time.Second, 5*time.Millisecond)
}

func TestRetriesTransientFailures(t *testing.T) {
	f := &fakeArgus{status: func(call int) int {
		if call <= 2 {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	}}
	s := newShipper(t, f, nil)
	s.Send(time.Now(), "survives")
	closeNow(s)
	require.Equal(t, []string{"survives"}, f.lines())
	require.EqualValues(t, 3, f.calls.Load(), "two failures then success")
	require.EqualValues(t, 0, s.Stats().Dropped)
}

func TestRateLimitedIsRetriedButBadKeyIsNot(t *testing.T) {
	f := &fakeArgus{status: func(call int) int {
		if call == 1 {
			return http.StatusTooManyRequests
		}
		return http.StatusOK
	}}
	s := newShipper(t, f, nil)
	s.Send(time.Now(), "x")
	closeNow(s)
	require.Equal(t, []string{"x"}, f.lines())

	f2 := &fakeArgus{status: func(int) int { return http.StatusUnauthorized }}
	s2 := newShipper(t, f2, nil)
	s2.Send(time.Now(), "y")
	closeNow(s2)
	require.EqualValues(t, 1, f2.calls.Load(), "401 is not retried")
	require.EqualValues(t, 1, s2.Stats().Dropped)
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	f := &fakeArgus{status: func(int) int { return http.StatusInternalServerError }}
	s := newShipper(t, f, func(c *Config) { c.MaxRetries = 2 })
	s.Send(time.Now(), "doomed")
	closeNow(s)
	require.EqualValues(t, 3, f.calls.Load(), "first try plus 2 retries")
	require.EqualValues(t, 1, s.Stats().Dropped)
}

// The whole point: a slow or dead Argus must never slow the application down.
func TestFullQueueDropsInsteadOfBlocking(t *testing.T) {
	f := &fakeArgus{block: make(chan struct{})}
	s := newShipper(t, f, func(c *Config) { c.QueueSize = 5; c.BatchSize = 2 })

	start := time.Now()
	for i := 0; i < 1000; i++ {
		s.Send(time.Now(), "line")
	}
	require.Less(t, time.Since(start), 500*time.Millisecond, "Send never blocks")
	require.Greater(t, s.Stats().Dropped, int64(900))
	close(f.block)
}

func TestNilShipperIsSafe(t *testing.T) {
	var s *Shipper
	require.False(t, s.Enabled())
	s.Send(time.Now(), "ignored")
	s.Close(context.Background())
	require.Equal(t, Stats{}, s.Stats())
	require.Nil(t, New(Config{URL: "http://x"}), "no key -> disabled")
	require.Nil(t, New(Config{APIKey: "k"}), "no url -> disabled")
	l := slog.New(s.Handler(slog.LevelInfo))
	l.Info("no panic") // handler over a nil shipper just drops
}

func TestSlogHandlerWritesJSONWithLowercaseLevelAndAttrs(t *testing.T) {
	f := &fakeArgus{}
	s := newShipper(t, f, nil)
	l := slog.New(s.Handler(slog.LevelInfo)).With("request_id", "r1").WithGroup("http")
	l.Debug("below level")
	l.Error("boom", slog.Int("status", 500))
	closeNow(s)

	lines := f.lines()
	require.Len(t, lines, 1, "debug is filtered")
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &m))
	require.Equal(t, "error", m["level"])
	require.Equal(t, "boom", m["msg"])
	require.Equal(t, "r1", m["request_id"])
	require.Equal(t, map[string]any{"status": float64(500)}, m["http"])
	require.False(t, strings.Contains(lines[0], "\n"))
}

func TestTeeSendsToEveryHandler(t *testing.T) {
	var a, b strings.Builder
	h := Tee(slog.NewTextHandler(&a, nil), slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelWarn}))
	l := slog.New(h).With("k", "v")
	l.Info("hello")
	l.Warn("careful")
	require.Contains(t, a.String(), "hello")
	require.Contains(t, a.String(), "careful")
	require.NotContains(t, b.String(), "hello", "second handler only wants warnings")
	require.Contains(t, b.String(), "careful")
	require.Contains(t, b.String(), "k=v")
}
