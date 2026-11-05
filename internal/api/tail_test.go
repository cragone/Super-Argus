package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/index"
	"github.com/cragone/Super-Argus/internal/ingest"
	"github.com/cragone/Super-Argus/internal/tail"
	"github.com/cragone/Super-Argus/internal/wal"
	"github.com/stretchr/testify/require"
)

type sseEvent struct{ name, data string }

type tailRig struct {
	t      *testing.T
	e      *env
	srv    *httptest.Server
	eng    *ingest.Engine
	ix     *index.Index
	hub    *tail.Hub
	token  string
	stream map[string]uint64
}

func newTailRig(t *testing.T, cfg tail.Config) *tailRig {
	t.Helper()
	e := newEnv(t)
	e.now = time.Now() // tokens must expire in the real future for streaming tests
	dir := t.TempDir()
	w, err := wal.Open(filepath.Join(dir, "wal"), time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	ix := index.New(e.srv.DB)
	eng := ingest.NewEngine(w, ix, chunk.NewLocalStore(filepath.Join(dir, "chunks")), ingest.DefaultEngineConfig())
	hub := tail.New(cfg)
	eng.SetPublisher(hub)
	e.srv.Tail, e.srv.Index = hub, ix
	e.h = NewRouter(e.srv)
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	return &tailRig{t: t, e: e, srv: srv, eng: eng, ix: ix, hub: hub, token: e.userToken("viewer"), stream: map[string]uint64{}}
}

func (r *tailRig) push(service, level, msg string) {
	r.t.Helper()
	r.pushRaw(service, fmt.Sprintf(`{"level":%q,"msg":%q}`, level, msg))
}

func (r *tailRig) pushRaw(service, line string) {
	r.t.Helper()
	id, ok := r.stream[service]
	if !ok {
		var err error
		id, err = r.ix.Resolve("k", map[string]string{"service": service}, 0)
		require.NoError(r.t, err)
		r.stream[service] = id
	}
	require.NoError(r.t, r.eng.Append([]wal.Record{{StreamID: id, TS: time.Now().UnixNano(), Line: line}}))
}

// open starts a tail and returns a channel of parsed events and a cancel func.
func (r *tailRig) open(q string, token string) (*http.Response, <-chan sseEvent, context.CancelFunc) {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", r.srv.URL+"/api/v1/tail?query="+url.QueryEscape(q), nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)
	r.t.Cleanup(func() { _ = res.Body.Close() })
	ch := make(chan sseEvent, 4096)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var ev sseEvent
		for sc.Scan() {
			switch l := sc.Text(); {
			case l == "":
				if ev.name != "" {
					ch <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(l, ": "):
				ch <- sseEvent{name: "comment", data: l[2:]}
			case strings.HasPrefix(l, "event: "):
				ev.name = l[7:]
			case strings.HasPrefix(l, "data: "):
				ev.data = l[6:]
			}
		}
	}()
	return res, ch, cancel
}

func next(t *testing.T, ch <-chan sseEvent, d time.Duration) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		require.True(t, ok, "stream closed")
		return ev
	case <-time.After(d):
		t.Fatal("timed out waiting for an event")
		return sseEvent{}
	}
}

func TestTailFiltersAndDeliversFast(t *testing.T) {
	r := newTailRig(t, tail.DefaultConfig())
	res, ch, _ := r.open(`{service="api"} | json | level="error"`, r.token)
	require.Equal(t, 200, res.StatusCode)
	require.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))
	require.Equal(t, "ready", next(t, ch, 2*time.Second).name)

	// non-matching: other service, wrong level
	r.push("web", "error", "other stream")
	r.push("api", "info", "wrong level")
	start := time.Now()
	r.push("api", "error", "boom")
	ev := next(t, ch, 2*time.Second)
	latency := time.Since(start)

	require.Equal(t, "entry", ev.name)
	var got tailEntry
	require.NoError(t, json.Unmarshal([]byte(ev.data), &got))
	require.Equal(t, `{"level":"error","msg":"boom"}`, got.Line)
	require.Equal(t, map[string]string{"service": "api"}, got.Labels)
	require.Equal(t, "error", got.Fields["level"])
	require.NotEmpty(t, got.TS)
	require.Less(t, latency, 500*time.Millisecond, "a pushed line must reach the client in under 500ms")
	t.Logf("push -> client latency: %v", latency)

	select {
	case extra := <-ch:
		t.Fatalf("unexpected event %+v", extra)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestTailRejections(t *testing.T) {
	r := newTailRig(t, tail.Config{PerUser: 1, Buffer: 8})
	get := func(q, token string) *http.Response {
		req, _ := http.NewRequest("GET", r.srv.URL+"/api/v1/tail?query="+url.QueryEscape(q), nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })
		return res
	}
	require.Equal(t, 401, get(`{a="b"}`, "").StatusCode)
	require.Equal(t, 400, get(`not a query`, r.token).StatusCode, "bad query")
	require.Equal(t, 400, get(`{a=~".*"}`, r.token).StatusCode, "a selector that matches everything is refused")

	_, ch, cancel := r.open(`{service="api"}`, r.token)
	require.Equal(t, "ready", next(t, ch, 2*time.Second).name)
	res := get(`{service="api"}`, r.token)
	require.Equal(t, 429, res.StatusCode, "per-user tail cap")
	require.Equal(t, "5", res.Header.Get("Retry-After"))

	cancel() // closing frees the slot
	require.Eventually(t, func() bool { return r.hub.Active() == 0 }, 2*time.Second, 10*time.Millisecond)
	_, ch2, _ := r.open(`{service="api"}`, r.token)
	require.Equal(t, "ready", next(t, ch2, 2*time.Second).name)
}

func TestTailHeartbeat(t *testing.T) {
	old := tailHeartbeat
	tailHeartbeat = 30 * time.Millisecond
	t.Cleanup(func() { tailHeartbeat = old })
	r := newTailRig(t, tail.DefaultConfig())
	_, ch, _ := r.open(`{service="api"}`, r.token)
	require.Equal(t, "ready", next(t, ch, 2*time.Second).name)
	require.Equal(t, sseEvent{"comment", "ping"}, next(t, ch, 2*time.Second))
}

func TestTailEndsWhenTokenExpires(t *testing.T) {
	r := newTailRig(t, tail.DefaultConfig())
	r.e.srv.Tokens.AccessTTL = 1500 * time.Millisecond // generous: login and connect must finish inside it, even under load
	short := r.e.userToken("editor")
	_, ch, _ := r.open(`{service="api"}`, short)
	require.Equal(t, "ready", next(t, ch, 5*time.Second).name)
	require.Equal(t, "expired", next(t, ch, 5*time.Second).name, "client is told to reconnect with a fresh token")
	_, ok := <-ch
	require.False(t, ok, "server closed the stream")
	require.Eventually(t, func() bool { return r.hub.Active() == 0 }, 2*time.Second, 10*time.Millisecond)
}

// A client that stops reading must never slow ingest down; when it resumes it
// is told how many lines it missed.
func TestStalledTailNeverBlocksIngest(t *testing.T) {
	r := newTailRig(t, tail.Config{PerUser: 5, Buffer: 4})
	_, ch, _ := r.open(`{service="api"}`, r.token)
	require.Equal(t, "ready", next(t, ch, 2*time.Second).name)

	// Stop consuming: ch is drained only after the burst, and 16 MB overruns the socket buffers.
	line := strings.Repeat("x", 1024)
	const lines = 16000
	start := time.Now()
	for i := 0; i < lines; i++ {
		r.pushRaw("api", line)
	}
	elapsed := time.Since(start)
	require.Less(t, elapsed, 5*time.Second, "ingest of %d lines took %v with a stalled tail", lines, elapsed)
	t.Logf("ingested %d lines in %v while a tail was stalled", lines, elapsed)

	// Resume reading (the reader goroutine already buffers into ch; drain it).
	dropped := 0
	deadline := time.After(8 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break loop
			}
			if ev.name == "dropped" {
				var d struct{ Count int }
				require.NoError(t, json.Unmarshal([]byte(ev.data), &d))
				dropped += d.Count
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	require.Positive(t, dropped, "the client must be told it missed lines")
	t.Logf("client was told %d lines were dropped", dropped)
}
