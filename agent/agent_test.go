package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServiceFromPod(t *testing.T) {
	cases := map[string]string{
		"api-7d9f8b6c5d-x2k4p":   "api",           // deployment
		"my-api-5f6c8d9b7-9xk2z": "my-api",        // deployment, 9-char hash
		"node-exporter-x2k4p":    "node-exporter", // daemonset
		"postgres-0":             "postgres",      // statefulset
		"my-redis":               "my-redis",      // "redis" has vowels, so it is not a generated suffix
		"standalone":             "standalone",
	}
	for pod, want := range cases {
		if got := serviceFromPod(pod); got != want {
			t.Errorf("serviceFromPod(%q) = %q, want %q", pod, got, want)
		}
	}
}

func TestPodDirLabels(t *testing.T) {
	l, ok := podDirLabels("shop_api-7d9f8b6c5d-x2k4p_0f1e2d3c", "web")
	if !ok || l["service"] != "api" || l["namespace"] != "shop" || l["pod"] != "api-7d9f8b6c5d-x2k4p" || l["container"] != "web" {
		t.Fatalf("labels = %v ok=%v", l, ok)
	}
	if _, ok := podDirLabels("not-a-pod-dir", "c"); ok {
		t.Fatal("expected a malformed directory to be rejected")
	}
}

func TestParseCRI(t *testing.T) {
	c, ok := parseCRI("2026-11-06T10:00:00.123456789Z stdout F hello world")
	if !ok || c.Partial || c.Text != "hello world" || c.TS.Nanosecond() != 123456789 {
		t.Fatalf("got %+v ok=%v", c, ok)
	}
	c, ok = parseCRI("2026-11-06T10:00:00Z stderr P part one ")
	if !ok || !c.Partial || c.Text != "part one " {
		t.Fatalf("got %+v ok=%v", c, ok)
	}
	for _, bad := range []string{"", "garbage", "2026-11-06T10:00:00Z stdout X nope", "notatime stdout F x"} {
		if _, ok := parseCRI(bad); ok {
			t.Errorf("parseCRI(%q) should fail", bad)
		}
	}
}

func TestParseJournalLine(t *testing.T) {
	raw := `{"MESSAGE":"started","_SYSTEMD_UNIT":"nginx.service","_HOSTNAME":"web1","__REALTIME_TIMESTAMP":"1790000000000000","__CURSOR":"s=abc"}`
	l, ts, line, cur, ok := parseJournalLine([]byte(raw))
	if !ok || line != "started" || cur != "s=abc" || l["service"] != "nginx" || l["unit"] != "nginx.service" || l["host"] != "web1" {
		t.Fatalf("labels=%v line=%q cursor=%q ok=%v", l, line, cur, ok)
	}
	if ts.UnixMicro() != 1790000000000000 {
		t.Fatalf("ts = %v", ts)
	}

	// byte-array message (non-UTF-8 in journald)
	_, _, line, _, ok = parseJournalLine([]byte(`{"MESSAGE":[104,105],"SYSLOG_IDENTIFIER":"cron"}`))
	if !ok || line != "hi" {
		t.Fatalf("line=%q ok=%v", line, ok)
	}

	// session scopes collapse into one service; entries without a message are skipped but keep their cursor
	l, _, _, _, _ = parseJournalLine([]byte(`{"MESSAGE":"x","_SYSTEMD_UNIT":"session-42.scope"}`))
	if l["service"] != "user-sessions" {
		t.Fatalf("service = %q", l["service"])
	}
	_, _, _, cur, ok = parseJournalLine([]byte(`{"__CURSOR":"s=def"}`))
	if ok || cur != "s=def" {
		t.Fatalf("cursor=%q ok=%v", cur, ok)
	}
}

// receiver is a fake Argus ingest endpoint collecting decoded NDJSON lines.
type receiver struct {
	mu    sync.Mutex
	lines []string
}

func (r *receiver) handler(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	if req.Header.Get("Content-Encoding") == "gzip" {
		body = gunzip(body)
	}
	r.mu.Lock()
	r.lines = append(r.lines, strings.Split(strings.TrimSpace(string(body)), "\n")...)
	r.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"accepted":1}`))
}

func (r *receiver) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A pod that appears after the agent started is discovered and its lines are
// shipped under the workload's service name, with partial lines joined.
func TestKubernetesDiscoversNewPod(t *testing.T) {
	rcv := &receiver{}
	srv := httptest.NewServer(http.HandlerFunc(rcv.handler))
	defer srv.Close()

	root := t.TempDir()
	st := LoadState(filepath.Join(t.TempDir(), "state.json"))
	hub := NewHub(srv.URL, "key", nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runKubernetes(ctx, hub, st, root, nil); close(done) }()

	time.Sleep(200 * time.Millisecond) // let the first (empty) scan finish
	dir := filepath.Join(root, "shop_api-7d9f8b6c5d-x2k4p_uid1", "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := "2026-11-06T10:00:00Z stdout F first line\n" +
		"2026-11-06T10:00:01Z stdout P long \n" +
		"2026-11-06T10:00:01Z stdout F line\n"
	if err := os.WriteFile(filepath.Join(dir, "0.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "shipped lines", func() bool {
		s := rcv.joined()
		return strings.Contains(s, "first line") && strings.Contains(s, "long line")
	})
	s := rcv.joined()
	for _, want := range []string{`"service":"api"`, `"namespace":"shop"`, `"container":"web"`} {
		if !strings.Contains(s, want) {
			t.Errorf("shipped payload missing %s:\n%s", want, s)
		}
	}

	cancel()
	<-done
	flush, c2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer c2()
	hub.Close(flush)
}

// Lines already in a file when the agent first starts are not replayed.
func TestKubernetesSkipsExistingBacklog(t *testing.T) {
	rcv := &receiver{}
	srv := httptest.NewServer(http.HandlerFunc(rcv.handler))
	defer srv.Close()

	root := t.TempDir()
	dir := filepath.Join(root, "shop_worker-0_uid2", "main")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "0.log")
	if err := os.WriteFile(path, []byte("2026-11-06T09:00:00Z stdout F old news\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := LoadState(filepath.Join(t.TempDir(), "state.json"))
	hub := NewHub(srv.URL, "key", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runKubernetes(ctx, hub, st, root, nil); close(done) }()

	time.Sleep(700 * time.Millisecond)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("2026-11-06T10:00:00Z stdout F fresh\n")
	_ = f.Close()

	waitFor(t, "fresh line", func() bool { return strings.Contains(rcv.joined(), "fresh") })
	if strings.Contains(rcv.joined(), "old news") {
		t.Fatal("backlog present at first start was replayed")
	}
	if !strings.Contains(rcv.joined(), `"service":"worker"`) {
		t.Fatalf("statefulset pod not mapped to its workload: %s", rcv.joined())
	}

	cancel()
	<-done
	flush, c2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer c2()
	hub.Close(flush)
}

func gunzip(b []byte) []byte {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return b
	}
	out, _ := io.ReadAll(zr)
	return out
}
