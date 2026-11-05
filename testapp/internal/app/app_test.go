package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// lowerLevel mirrors what the Argus handler does, so tests see the same `level` values Argus queries do.
func lowerLevel(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.LevelKey {
		a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
	}
	return a
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// lines returns the JSON log records written so far.
func (s *syncBuf) lines(t *testing.T) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s.b.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(l), &m), l)
		out = append(out, m)
	}
	return out
}

func (s *syncBuf) find(t *testing.T, msg string) []map[string]any {
	var out []map[string]any
	for _, m := range s.lines(t) {
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

type rig struct {
	t   *testing.T
	h   http.Handler
	log *syncBuf
	db  *gorm.DB
}

func newRig(t *testing.T) *rig {
	t.Helper()
	gin.SetMode(gin.TestMode)
	buf := &syncBuf{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: lowerLevel}))
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "t.db")), &gorm.Config{Logger: NewGormLogger(logger)})
	require.NoError(t, err)
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, Migrate(db))
	a := &App{DB: db, Log: logger}
	return &rig{t: t, h: a.Router(), log: buf, db: db}
}

func (r *rig) do(method, path string, body any, hdr ...string) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

func TestItemsCRUD(t *testing.T) {
	r := newRig(t)

	rec := r.do("POST", "/items", map[string]any{"name": "  anvil ", "qty": 3})
	require.Equal(t, 201, rec.Code, rec.Body.String())
	var it Item
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &it))
	require.Equal(t, "anvil", it.Name, "name is trimmed")
	require.NotZero(t, it.ID)

	require.Equal(t, 200, r.do("GET", "/items/"+itoa(it.ID), nil).Code)
	require.Equal(t, 404, r.do("GET", "/items/99999", nil).Code)

	r.do("POST", "/items", map[string]any{"name": "bolt", "qty": 1})
	rec = r.do("GET", "/items?q=bol", nil)
	require.Equal(t, 200, rec.Code)
	var list struct {
		Count int    `json:"count"`
		Items []Item `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Equal(t, 1, list.Count)
	require.Equal(t, "bolt", list.Items[0].Name)

	require.Equal(t, 204, r.do("DELETE", "/items/"+itoa(it.ID), nil).Code)
	require.Equal(t, 404, r.do("DELETE", "/items/"+itoa(it.ID), nil).Code)
}

func itoa(n uint) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestItemValidation(t *testing.T) {
	r := newRig(t)
	require.Equal(t, 400, r.do("POST", "/items", nil).Code, "no body")
	require.Equal(t, 422, r.do("POST", "/items", map[string]any{"name": " ", "qty": 1}).Code)
	require.Equal(t, 422, r.do("POST", "/items", map[string]any{"name": "x", "qty": -1}).Code)
	require.Equal(t, 422, r.do("POST", "/items", map[string]any{"name": strings.Repeat("n", 101)}).Code)
}

func TestSeedIsBounded(t *testing.T) {
	r := newRig(t)
	require.Equal(t, 201, r.do("POST", "/seed?n=7", nil).Code)
	var n int64
	r.db.Model(&Item{}).Count(&n)
	require.EqualValues(t, 7, n)
	require.Equal(t, 201, r.do("POST", "/seed?n=999999", nil).Code)
	r.db.Model(&Item{}).Count(&n)
	require.EqualValues(t, 7+500, n, "capped at 500")
	require.Equal(t, 201, r.do("POST", "/seed?n=abc", nil).Code, "bad n falls back to the default")
}

func TestRequestLogLinesCarryRouteStatusAndLevel(t *testing.T) {
	r := newRig(t)
	r.do("GET", "/healthz", nil)
	r.do("GET", "/items/424242", nil, "X-Request-Id", "req-abc")
	r.do("GET", "/error", nil)
	r.do("GET", "/no/such/route/12345", nil)

	reqs := r.log.find(t, "request")
	require.Len(t, reqs, 4)
	byRoute := map[string]map[string]any{}
	for _, m := range reqs {
		byRoute[m["route"].(string)] = m
	}

	require.Equal(t, "info", byRoute["/healthz"]["level"])
	require.EqualValues(t, 200, byRoute["/healthz"]["status"])
	require.Contains(t, byRoute["/healthz"], "latency_ms")

	nf := byRoute["/items/:id"]
	require.Equal(t, "warn", nf["level"], "4xx is a warning")
	require.EqualValues(t, 404, nf["status"])
	require.Equal(t, "req-abc", nf["request_id"], "caller's request id is kept")
	require.Equal(t, "/items/424242", nf["path"], "the raw path is a field, the route template is what you group by")

	require.Equal(t, "error", byRoute["/error"]["level"], "5xx is an error")
	require.Equal(t, "warn", byRoute["unmatched"]["level"], "unknown paths share one bounded route value")
}

func TestRequestIDIsGeneratedAndEchoed(t *testing.T) {
	r := newRig(t)
	rec := r.do("GET", "/healthz", nil)
	id := rec.Header().Get("X-Request-Id")
	require.Len(t, id, 16)
	require.NotEqual(t, id, r.do("GET", "/healthz", nil).Header().Get("X-Request-Id"))
	require.Equal(t, "mine", r.do("GET", "/healthz", nil, "X-Request-Id", "mine").Header().Get("X-Request-Id"))
	require.NotEqual(t, strings.Repeat("x", 200), r.do("GET", "/healthz", nil, "X-Request-Id", strings.Repeat("x", 200)).Header().Get("X-Request-Id"), "oversized ids are replaced")
}

func TestFailureRoutes(t *testing.T) {
	r := newRig(t)

	require.Equal(t, 500, r.do("GET", "/error", nil).Code)
	require.Len(t, r.log.find(t, "unexpected failure"), 1)

	require.Equal(t, 502, r.do("GET", "/flaky?p=1", nil).Code)
	require.Len(t, r.log.find(t, "upstream dependency failed"), 1)
	require.Equal(t, 200, r.do("GET", "/flaky?p=0", nil).Code)

	rec := r.do("GET", "/panic", nil)
	require.Equal(t, 500, rec.Code)
	require.Contains(t, rec.Body.String(), "request_id")
	p := r.log.find(t, "panic recovered")
	require.Len(t, p, 1)
	require.Contains(t, p[0]["stack"], "goroutine")
	require.Equal(t, "error", p[0]["level"])
	require.Equal(t, 200, r.do("GET", "/healthz", nil).Code, "the server survives a panic")
}

func TestSlowAndNoise(t *testing.T) {
	r := newRig(t)
	start := time.Now()
	rec := r.do("GET", "/slow?ms=120", nil)
	require.Equal(t, 200, rec.Code)
	require.GreaterOrEqual(t, time.Since(start), 120*time.Millisecond)
	require.Empty(t, r.log.find(t, "slow handler"), "under 500ms is not flagged")

	rec = r.do("GET", "/noise?n=40", nil)
	require.Equal(t, 200, rec.Code)
	levels := map[any]int{}
	for _, m := range r.log.lines(t) {
		if _, ok := m["job"]; ok {
			levels[m["level"]]++
		}
	}
	total := 0
	for _, n := range levels {
		total += n
	}
	require.Equal(t, 40, total)
	require.Greater(t, len(levels), 1, "mixed levels")
}

func TestSlowHandlerStopsWhenClientCancels(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/slow?ms=5000", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		r.h.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler kept sleeping after the client went away")
	}
	require.Len(t, r.log.find(t, "client gave up on slow request"), 1)
}

func TestGormQueriesAreLoggedWithDuration(t *testing.T) {
	r := newRig(t)
	r.do("POST", "/items", map[string]any{"name": "logged", "qty": 1})
	qs := r.log.find(t, "query")
	require.NotEmpty(t, qs)
	var insert map[string]any
	for _, q := range qs {
		if strings.Contains(q["sql"].(string), "INSERT INTO `items`") {
			insert = q
		}
	}
	require.NotNil(t, insert, "the INSERT is logged")
	require.EqualValues(t, 1, insert["rows"])
	require.Contains(t, insert, "duration_ms")
	require.Equal(t, "gorm", insert["component"])
}

func TestGormLoggerLevels(t *testing.T) {
	buf := &syncBuf{}
	l := NewGormLogger(slog.New(slog.NewJSONHandler(buf, nil)))
	fc := func() (string, int64) { return "SELECT 1", 1 }

	l.Trace(context.Background(), time.Now().Add(-SlowQuery-100*time.Millisecond), fc, nil)
	l.Trace(context.Background(), time.Now(), fc, errors.New("no such table"))
	l.Trace(context.Background(), time.Now(), fc, gorm.ErrRecordNotFound)
	got := buf.lines(t)
	require.Len(t, got, 3)
	require.Equal(t, "slow query", got[0]["msg"])
	require.Equal(t, "WARN", got[0]["level"])
	require.Equal(t, "query failed", got[1]["msg"])
	require.Equal(t, "no such table", got[1]["error"])
	require.Equal(t, "query", got[2]["msg"], "a not-found lookup is an ordinary query, not an error")
	require.Equal(t, "INFO", got[2]["level"])

	buf2 := &syncBuf{}
	quiet := NewGormLogger(slog.New(slog.NewJSONHandler(buf2, nil))).LogMode(1) // Silent
	quiet.Trace(context.Background(), time.Now(), fc, errors.New("x"))
	require.Empty(t, buf2.lines(t))
}
