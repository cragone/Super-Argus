package argus

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// flaky is an http.Client transport whose server can be refusing connections,
// black-holed (connects hang) or healthy.
type flaky struct {
	mode atomic.Int32 // 0 healthy, 1 refused, 2 black hole
	addr string
}

func (f *flaky) client(timeout time.Duration) *http.Client {
	d := &net.Dialer{}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			switch f.mode.Load() {
			case 1:
				return nil, errors.New("dial tcp: connection refused")
			case 2: // like a real black hole, the connect attempt eventually times out on its own
				select {
				case <-ctx.Done():
				case <-time.After(300 * time.Millisecond):
				}
				return nil, errors.New("dial tcp: i/o timeout")
			}
			return d.DialContext(ctx, network, f.addr)
		},
	}}
}

// An application that logs through the SDK must not notice Argus being down:
// no call blocks, memory stays bounded, no goroutines pile up, and lines flow
// again once the server returns.
func TestServerDowntimeDoesNotAffectApplication(t *testing.T) {
	fa := &fakeArgus{}
	srv := httptest.NewServer(fa.handler(t))
	t.Cleanup(srv.Close)
	fl := &flaky{addr: srv.Listener.Addr().String()}

	errs := atomic.Int32{}
	s := New(Config{
		URL: "http://argus.invalid", APIKey: "k", Labels: map[string]string{"service": "app"},
		BatchSize: 100, FlushEvery: 10 * time.Millisecond, QueueSize: 1000,
		MaxRetries: 1, Backoff: 5 * time.Millisecond, Client: fl.client(50 * time.Millisecond),
		OnError: func(string) { errs.Add(1) },
	})
	log := slog.New(s.Handler(slog.LevelInfo))
	startGoroutines := runtime.NumGoroutine()

	var worst time.Duration
	logBurst := func(n int, tag string) {
		for i := 0; i < n; i++ {
			t0 := time.Now()
			log.Info("request handled", "tag", tag, "i", i)
			worst = max(worst, time.Since(t0))
		}
	}

	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	fl.mode.Store(1) // connection refused
	logBurst(50_000, "refused")
	fl.mode.Store(2) // black hole: connects hang until the client timeout
	logBurst(50_000, "blackhole")

	require.Less(t, worst, 100*time.Millisecond, "a log call blocked on the dead server")
	require.Greater(t, s.Stats().Dropped, int64(50_000), "excess lines are dropped and counted, not buffered")
	var during runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&during)
	require.Less(t, int64(during.HeapAlloc)-int64(before.HeapAlloc), int64(20<<20), "memory is bounded by the queue")
	// One send is always in flight while the queue is saturated (a few transport goroutines),
	// but the count must be a small constant, not proportional to the 100,000 lines logged.
	require.LessOrEqual(t, runtime.NumGoroutine(), startGoroutines+30, "goroutines grew with the number of failed sends")

	fl.mode.Store(0) // Argus is back
	require.Eventually(t, func() bool {
		logBurst(1, "recovered")
		for _, l := range fa.lines() {
			if len(l) > 0 && contains(l, `"tag":"recovered"`) {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "logs flow again after recovery")

	s.Close(context.Background())
	require.Positive(t, errs.Load(), "drops were reported through OnError")
	require.Eventually(t, func() bool { return runtime.NumGoroutine() <= startGoroutines+2 }, 2*time.Second, 20*time.Millisecond, "worker exits on Close")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRetryAfterIsHonouredButCapped(t *testing.T) {
	f := &fakeArgus{}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		f.handler(t).ServeHTTP(w, r)
	}))
	defer srv.Close()
	s := New(Config{URL: srv.URL, APIKey: "k", FlushEvery: 5 * time.Millisecond, Backoff: time.Millisecond, MaxRetryWait: 120 * time.Millisecond})
	start := time.Now()
	s.Send(time.Now(), "slow down")
	require.Eventually(t, func() bool { return len(f.lines()) == 1 }, 10*time.Second, 5*time.Millisecond)
	took := time.Since(start)
	s.Close(context.Background())
	require.Equal(t, []string{"slow down"}, f.lines())
	require.GreaterOrEqual(t, took, 100*time.Millisecond, "waited out (a capped) Retry-After instead of hammering")
	require.Less(t, took, 5*time.Second, "the cap stopped a 30s Retry-After from stalling delivery")
}

func TestLongLinesAreTruncatedAndLabelsAreCopied(t *testing.T) {
	f := &fakeArgus{}
	labels := map[string]string{"service": "a"}
	s := newShipper(t, f, func(c *Config) { c.MaxLineBytes = 10; c.Labels = labels })
	labels["service"] = "mutated-later" // must not leak into queued entries
	labels["extra"] = "x"
	s.Send(time.Now(), "0123456789ABCDEF")
	closeNow(s)
	require.Equal(t, []string{"0123456789...[truncated]"}, f.lines())
	require.Equal(t, map[string]string{"service": "a"}, f.got[0].entries[0].Labels)
}
