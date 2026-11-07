// Package argus ships structured logs to an Argus server.
//
// It is deliberately small and safe to put in a request path:
//
//   - logging never blocks: when the in-memory queue is full the line is
//     dropped and counted, so a slow or dead server cannot slow the application;
//   - memory is bounded by QueueSize lines of at most MaxLineBytes each;
//   - lines are batched and gzipped; failed sends are retried with exponential
//     backoff (honouring Retry-After) and then dropped, never held forever;
//   - the standard library is the only dependency.
//
// Typical use:
//
//	ship := argus.New(argus.Config{URL: "http://argus:8080", APIKey: key,
//		Labels: map[string]string{"service": "api", "env": "prod"}})
//	defer ship.Close(context.Background())
//	log := slog.New(argus.Tee(slog.NewTextHandler(os.Stderr, nil), ship.Handler(slog.LevelInfo)))
package argus

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config configures a Shipper.
type Config struct {
	// URL is the Argus base URL, e.g. http://localhost:8080.
	URL string
	// APIKey is an ingest key (create one as admin via POST /api/v1/api-keys).
	APIKey string
	// Labels identify the stream. Keep them low-cardinality: never user, request or session IDs.
	Labels map[string]string

	BatchSize  int           // lines per request (default 200)
	FlushEvery time.Duration // flush a partial batch after this long (default 1s)
	QueueSize  int           // lines buffered before dropping (default 10,000)
	MaxRetries int           // retries per batch for 429/5xx/network errors (default 3)
	Backoff    time.Duration // first retry delay, doubled each time (default 250ms)
	// MaxRetryWait caps any single wait, including a server's Retry-After (default 30s).
	MaxRetryWait time.Duration
	// MaxLineBytes truncates longer lines before queueing, bounding memory (default 64 KiB).
	MaxLineBytes int
	// OnError is called (from the worker goroutine) when lines are dropped
	// after failed delivery. The default logs at most once every 30 seconds.
	OnError func(msg string)
	Client  *http.Client
}

// Stats are cumulative counters.
type Stats struct {
	Sent    int64 // lines accepted by Argus
	Dropped int64 // lines discarded because the queue was full or sending kept failing
	Batches int64 // successful requests
}

type entry struct {
	TS     string            `json:"ts"`
	Labels map[string]string `json:"labels"`
	Line   string            `json:"line"`
}

// Shipper batches log lines and sends them to Argus in the background.
type Shipper struct {
	cfg   Config
	queue chan entry

	sent, dropped, batches atomic.Int64

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// New starts a shipper. A nil Shipper (no URL or key) is valid: Enabled reports false.
func New(cfg Config) *Shipper {
	if cfg.URL == "" || cfg.APIKey == "" {
		return nil
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 200
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = time.Second
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 10_000
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	} else if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = 250 * time.Millisecond
	}
	if cfg.MaxRetryWait <= 0 {
		cfg.MaxRetryWait = 30 * time.Second
	}
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = 64 << 10
	}
	if cfg.OnError == nil {
		cfg.OnError = newThrottledLog(30 * time.Second)
	}
	if cfg.Client == nil {
		// Explicit dial and handshake timeouts so a black-holed server cannot
		// leave connection attempts hanging around for the OS default.
		cfg.Client = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        2,
			IdleConnTimeout:     60 * time.Second,
		}}
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	labels := make(map[string]string, len(cfg.Labels)) // the caller may keep mutating its map
	for k, v := range cfg.Labels {
		labels[k] = v
	}
	cfg.Labels = labels
	s := &Shipper{cfg: cfg, queue: make(chan entry, cfg.QueueSize), stop: make(chan struct{}), done: make(chan struct{})}
	go s.run()
	return s
}

// Enabled reports whether logs are being shipped.
func (s *Shipper) Enabled() bool { return s != nil }

// Stats returns the cumulative counters.
func (s *Shipper) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	return Stats{Sent: s.sent.Load(), Dropped: s.dropped.Load(), Batches: s.batches.Load()}
}

// Send queues one line without ever blocking the caller.
func (s *Shipper) Send(ts time.Time, line string) {
	if s == nil {
		return
	}
	if len(line) > s.cfg.MaxLineBytes {
		line = line[:s.cfg.MaxLineBytes] + "...[truncated]"
	}
	e := entry{TS: ts.UTC().Format(time.RFC3339Nano), Labels: s.cfg.Labels, Line: line}
	select {
	case s.queue <- e:
	default:
		s.dropped.Add(1)
	}
}

// Close flushes what is queued (waiting up to ctx) and stops the worker.
func (s *Shipper) Close(ctx context.Context) {
	if s == nil {
		return
	}
	s.once.Do(func() { close(s.stop) })
	select {
	case <-s.done:
	case <-ctx.Done():
	}
}

func (s *Shipper) run() {
	defer close(s.done)
	t := time.NewTicker(s.cfg.FlushEvery)
	defer t.Stop()
	batch := make([]entry, 0, s.cfg.BatchSize)
	flush := func() {
		if len(batch) > 0 {
			s.deliver(batch)
			batch = batch[:0]
		}
	}
	for {
		select {
		case e := <-s.queue:
			batch = append(batch, e)
			if len(batch) >= s.cfg.BatchSize {
				flush()
			}
		case <-t.C:
			flush()
		case <-s.stop:
			for { // drain what is already queued, then stop
				select {
				case e := <-s.queue:
					batch = append(batch, e)
					if len(batch) >= s.cfg.BatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func encode(batch []entry) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(zw)
	for _, e := range batch {
		if err := enc.Encode(e); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// deliver sends one batch, retrying transient failures, and drops it if they persist.
func (s *Shipper) deliver(batch []entry) {
	body, err := encode(batch)
	if err != nil {
		s.dropped.Add(int64(len(batch)))
		return
	}
	delay := s.cfg.Backoff
	for attempt := 0; ; attempt++ {
		status, accepted, retryAfter, err := s.post(body)
		switch {
		case err == nil && status == http.StatusOK:
			s.sent.Add(int64(accepted))
			s.dropped.Add(int64(len(batch) - accepted)) // lines Argus rejected (too old, bad labels, ...)
			s.batches.Add(1)
			return
		case err == nil && status >= 400 && status < 500 && status != http.StatusTooManyRequests:
			// Our fault (bad key, malformed): retrying cannot help.
			s.cfg.OnError(fmt.Sprintf("argus: dropping %d lines: server answered %d", len(batch), status))
			s.dropped.Add(int64(len(batch)))
			return
		}
		if attempt >= s.cfg.MaxRetries {
			s.cfg.OnError(fmt.Sprintf("argus: dropping %d lines after %d attempts (last: status %d, err %v)", len(batch), attempt+1, status, err))
			s.dropped.Add(int64(len(batch)))
			return
		}
		wait := min(max(delay, retryAfter), s.cfg.MaxRetryWait)
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-s.stop: // shutting down: try once more immediately rather than waiting
			t.Stop()
		}
		delay *= 2
	}
}

func (s *Shipper) post(body []byte) (status, accepted int, retryAfter time.Duration, err error) {
	req, err := http.NewRequest(http.MethodPost, s.cfg.URL+"/api/v1/ingest/push", bytes.NewReader(body))
	if err != nil {
		return 0, 0, 0, err
	}
	req.Header.Set("X-Argus-Key", s.cfg.APIKey)
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Content-Encoding", "gzip")
	res, err := s.cfg.Client.Do(req)
	if err != nil {
		return 0, 0, 0, err
	}
	defer res.Body.Close()
	if secs, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && secs > 0 {
		retryAfter = time.Duration(secs) * time.Second
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusOK {
		var out struct {
			Accepted int      `json:"accepted"`
			Rejected int      `json:"rejected"`
			Errors   []string `json:"errors"`
		}
		if json.Unmarshal(raw, &out) == nil {
			if out.Rejected > 0 {
				s.cfg.OnError(fmt.Sprintf("argus: %d lines rejected: %s", out.Rejected, strings.Join(out.Errors, "; ")))
			}
			return res.StatusCode, out.Accepted, 0, nil
		}
	}
	return res.StatusCode, 0, retryAfter, nil
}

// newThrottledLog returns an OnError that logs at most once per interval, so a
// long outage cannot flood the host application's own logs.
func newThrottledLog(every time.Duration) func(string) {
	var mu sync.Mutex
	var last time.Time
	return func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(last) >= every {
			last = time.Now()
			log.Print(msg)
		}
	}
}

// String describes the shipper for startup logs.
func (s *Shipper) String() string {
	if s == nil {
		return "argus shipping disabled"
	}
	return fmt.Sprintf("shipping to %s as %v", s.cfg.URL, s.cfg.Labels)
}
