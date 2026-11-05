package ingest

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/auth"
	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/db"
	"github.com/cragone/Super-Argus/internal/index"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/cragone/Super-Argus/internal/wal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// rig is a full write path in a temp dir with an injectable clock.
type rig struct {
	t      *testing.T
	dir    string
	g      *gorm.DB
	now    time.Time
	ix     *index.Index
	store  *chunk.LocalStore
	w      *wal.WAL
	eng    *Engine
	h      *Handler
	apiKey string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, dir: t.TempDir(), now: t0}
	g, err := db.Open("sqlite:" + filepath.Join(r.dir, "t.db"))
	require.NoError(t, err)
	sqlDB, _ := g.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Migrate(g))
	r.g = g
	key, prefix, hash, err := auth.GenerateAPIKey()
	require.NoError(t, err)
	require.NoError(t, g.Create(&models.APIKey{Name: "k", KeyHash: hash, KeyPrefix: prefix, AllowedLabels: "{}"}).Error)
	r.apiKey = key
	r.start()
	return r
}

// start opens WAL/engine/handler over the rig's directory (also used to simulate a restart).
func (r *rig) start() {
	r.t.Helper()
	w, err := wal.Open(filepath.Join(r.dir, "wal"), time.Hour) // background sync off; fsync is covered in the wal tests
	require.NoError(r.t, err)
	r.t.Cleanup(func() { _ = w.Close() })
	r.w = w
	r.ix = index.New(r.g)
	r.ix.Now = func() time.Time { return r.now }
	r.store = chunk.NewLocalStore(filepath.Join(r.dir, "chunks"))
	cfg := DefaultEngineConfig()
	cfg.Now = func() time.Time { return r.now }
	r.eng = NewEngine(w, r.ix, r.store, cfg)
	require.NoError(r.t, r.eng.Recover())
	r.h = NewHandler(r.g, r.eng, r.ix, DefaultLimits())
	r.h.Now = func() time.Time { return r.now }
}

// crash drops the engine without flushing, like kill -9 (heads are lost, the WAL stays).
func (r *rig) crash() { require.NoError(r.t, r.w.Close()) }

func (r *rig) push(body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/push", strings.NewReader(body))
	req.Header.Set(KeyHeader, r.apiKey)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.h.Push(rec, req)
	return rec
}

func line(ts time.Time, svc, msg string) string {
	b, _ := json.Marshal(map[string]any{"ts": ts.Format(time.RFC3339Nano), "labels": map[string]string{"service": svc}, "line": msg})
	return string(b) + "\n"
}

func (r *rig) storedLines() map[string]int {
	r.t.Helper()
	var cs []models.Chunk
	require.NoError(r.t, r.g.Find(&cs).Error)
	got := map[string]int{}
	for _, c := range cs {
		data, err := r.store.Get(c.Path)
		require.NoError(r.t, err)
		rd, err := chunk.NewReader(data)
		require.NoError(r.t, err)
		require.Equal(r.t, c.LineCount, rd.Meta.Lines)
		require.NoError(r.t, rd.Scan(0, 1<<62, func(e chunk.Entry) bool { got[e.Line]++; return true }))
	}
	return got
}

func TestPushFlushesToChunkAndIndex(t *testing.T) {
	r := newRig(t)
	var body strings.Builder
	for i := 0; i < 100; i++ {
		body.WriteString(line(r.now.Add(-time.Duration(i)*time.Second), "api", fmt.Sprintf("msg %d", i)))
	}
	rec := r.push(body.String(), nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"accepted":100,"rejected":0}`, rec.Body.String())
	require.Equal(t, 100, r.eng.HeadLines())

	// not yet old/large enough: nothing flushed
	require.NoError(t, r.eng.Tick())
	require.Equal(t, 100, r.eng.HeadLines())

	r.now = r.now.Add(time.Hour + time.Second) // head age passes
	require.NoError(t, r.eng.Tick())
	require.Zero(t, r.eng.HeadLines())

	var cs []models.Chunk
	require.NoError(t, r.g.Find(&cs).Error)
	require.Len(t, cs, 1)
	require.Equal(t, 100, cs[0].LineCount)
	require.FileExists(t, filepath.Join(r.dir, "chunks", filepath.FromSlash(cs[0].Path)))
	require.Len(t, r.storedLines(), 100)

	var st models.Stream
	require.NoError(t, r.g.First(&st).Error)
	require.Equal(t, `service="api"`, st.LabelsText)
	require.WithinDuration(t, t0, st.LastSeen, time.Second, "last_seen is when the stream was last pushed to")
}

func TestSizeTriggeredFlush(t *testing.T) {
	r := newRig(t)
	big := strings.Repeat("x", 30<<10)
	var body strings.Builder
	for i := 0; i < 40; i++ { // ~1.2 MB
		body.WriteString(line(r.now, "api", fmt.Sprintf("%d%s", i, big)))
	}
	require.Equal(t, 200, r.push(body.String(), nil).Code)
	require.NoError(t, r.eng.Tick())
	require.Zero(t, r.eng.HeadLines())
}

func TestCrashRecoveryLosesNothingAcknowledged(t *testing.T) {
	r := newRig(t)
	var want []string
	for batch := 0; batch < 5; batch++ {
		var body strings.Builder
		for i := 0; i < 50; i++ {
			msg := fmt.Sprintf("b%d-%d", batch, i)
			want = append(want, msg)
			body.WriteString(line(r.now, fmt.Sprintf("svc%d", i%3), msg))
		}
		require.Equal(t, 200, r.push(body.String(), nil).Code)
	}
	r.crash() // acknowledged but never flushed

	r.start() // restart: replay WAL -> chunks
	got := r.storedLines()
	require.Len(t, got, len(want))
	for _, m := range want {
		require.Equal(t, 1, got[m], m)
	}
	segs, _ := os.ReadDir(filepath.Join(r.dir, "wal"))
	require.Len(t, segs, 1, "replayed segments are dropped, only the new active one remains")

	// a clean second restart replays nothing, so nothing is duplicated
	require.NoError(t, r.eng.Close())
	r.start()
	require.Len(t, r.storedLines(), len(want))
}

func TestCleanShutdownFlushesHeads(t *testing.T) {
	r := newRig(t)
	require.Equal(t, 200, r.push(line(r.now, "api", "bye"), nil).Code)
	require.NoError(t, r.eng.Close())
	r.start()
	require.Equal(t, map[string]int{"bye": 1}, r.storedLines())
}

func TestWALTrimmedAfterFlush(t *testing.T) {
	r := newRig(t)
	require.Equal(t, 200, r.push(line(r.now, "api", "a"), nil).Code)
	r.now = r.now.Add(2 * time.Hour)
	require.NoError(t, r.eng.Tick())
	segs, _ := os.ReadDir(filepath.Join(r.dir, "wal"))
	require.Len(t, segs, 1)
	require.Equal(t, int64(0), mustInfo(t, filepath.Join(r.dir, "wal", segs[0].Name())).Size(), "active segment is fresh/empty")
}

func mustInfo(t *testing.T, p string) os.FileInfo {
	fi, err := os.Stat(p)
	require.NoError(t, err)
	return fi
}

func TestAuth(t *testing.T) {
	r := newRig(t)
	for name, key := range map[string]string{"missing": "", "wrong": "argus_nope"} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(line(r.now, "a", "x")))
		if key != "" {
			req.Header.Set(KeyHeader, key)
		}
		rec := httptest.NewRecorder()
		r.h.Push(rec, req)
		require.Equal(t, 401, rec.Code, name)
	}
	require.NoError(t, r.g.Model(&models.APIKey{}).Where("1=1").Update("revoked_at", r.now).Error)
	require.Equal(t, 401, r.push(line(r.now, "a", "x"), nil).Code, "revoked key")
}

func TestValidation(t *testing.T) {
	r := newRig(t)
	long := strings.Repeat("v", 257)
	many := map[string]string{}
	for i := 0; i < 16; i++ {
		many[fmt.Sprintf("l%d", i)] = "v"
	}
	manyJSON, _ := json.Marshal(many)
	huge := strings.Repeat("é", 40000) // 80,000 bytes

	body := strings.Join([]string{
		`{"ts":"` + r.now.Add(-25*time.Hour).Format(time.RFC3339) + `","labels":{"a":"b"},"line":"too old"}`,
		`{"ts":"` + r.now.Add(6*time.Minute).Format(time.RFC3339) + `","labels":{"a":"b"},"line":"future"}`,
		`{"labels":{"a":"` + long + `"},"line":"long label"}`,
		`{"labels":{"bad-name":"v"},"line":"bad name"}`,
		`{"labels":` + string(manyJSON) + `,"line":"16 labels"}`,
		`{"labels":{},"line":"no labels"}`,
		`{"labels":{"a":"b"}}`,
		`not json`,
		`{"ts":"garbage","labels":{"a":"b"},"line":"bad ts"}`,
		`{"labels":{"a":"b"},"line":"ok, no ts"}`,
		`{"ts":` + fmt.Sprint(r.now.UnixNano()) + `,"labels":{"a":"b"},"line":"ok, epoch ns"}`,
		`{"ts":"` + fmt.Sprint(r.now.UnixNano()) + `","labels":{"a":"b"},"line":"ok, epoch string"}`,
		`{"labels":{"a":"b"},"line":"` + huge + `"}`,
	}, "\n")
	rec := r.push(body, nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var resp pushResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 4, resp.Accepted)
	require.Equal(t, 9, resp.Rejected)
	require.Len(t, resp.Errors, 9)
	require.Contains(t, resp.Errors[0], "line 1: timestamp older")

	require.NoError(t, r.eng.FlushAll())
	got := r.storedLines()
	require.Contains(t, got, "ok, no ts")
	var truncated string
	for l := range got {
		if strings.HasSuffix(l, truncMarker) {
			truncated = l
		}
	}
	require.NotEmpty(t, truncated, "oversized line is truncated with a marker")
	require.LessOrEqual(t, len(truncated), 64<<10)

	// all-invalid batch -> 400
	require.Equal(t, 400, r.push(`not json`, nil).Code)
}

func TestHeaderLabelsMergeAndOverride(t *testing.T) {
	r := newRig(t)
	body := `{"labels":{"env":"dev"},"line":"one"}` + "\n" + `{"line":"two"}` + "\n"
	rec := r.push(body, map[string]string{LabelsHeader: "service=api, env=prod"})
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var ss []models.Stream
	require.NoError(t, r.g.Order("labels_text").Find(&ss).Error)
	require.Len(t, ss, 2)
	require.Equal(t, `env="dev",service="api"`, ss[0].LabelsText)
	require.Equal(t, `env="prod",service="api"`, ss[1].LabelsText)

	require.Equal(t, 400, r.push(`{"line":"x"}`, map[string]string{LabelsHeader: "broken"}).Code)
}

func TestGzipAndBodyLimits(t *testing.T) {
	r := newRig(t)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(line(r.now, "api", "zipped")))
	require.NoError(t, gz.Close())
	req := httptest.NewRequest(http.MethodPost, "/", &buf)
	req.Header.Set(KeyHeader, r.apiKey)
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	r.h.Push(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())

	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not gzip"))
	req.Header.Set(KeyHeader, r.apiKey)
	req.Header.Set("Content-Encoding", "gzip")
	rec = httptest.NewRecorder()
	r.h.Push(rec, req)
	require.Equal(t, 400, rec.Code)

	r.h.Limits.MaxBodyBytes = 1000
	require.Equal(t, 413, r.push(strings.Repeat("a", 2000), nil).Code)

	// a gzip bomb is bounded by the decompressed size
	buf.Reset()
	gz = gzip.NewWriter(&buf)
	_, _ = gz.Write(bytes.Repeat([]byte("a"), 1<<20))
	require.NoError(t, gz.Close())
	r.h.Limits.MaxBodyBytes = 10 << 10
	req = httptest.NewRequest(http.MethodPost, "/", &buf)
	req.Header.Set(KeyHeader, r.apiKey)
	req.Header.Set("Content-Encoding", "gzip")
	rec = httptest.NewRecorder()
	r.h.Push(rec, req)
	require.Equal(t, 413, rec.Code)
}

func TestRateLimit(t *testing.T) {
	r := newRig(t)
	r.h.Limits.DefaultLinesPerSec = 10 // capacity 50 with burst 5
	batch := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(line(r.now, "api", "x"))
		}
		return b.String()
	}
	require.Equal(t, 200, r.push(batch(40), nil).Code)
	rec := r.push(batch(20), nil) // only 10 tokens left
	require.Equal(t, 429, rec.Code)
	require.Equal(t, "1", rec.Header().Get("Retry-After"))
	require.Equal(t, 413, r.push(batch(60), nil).Code, "can never fit the bucket")

	r.now = r.now.Add(2 * time.Second) // +20 tokens -> 30
	require.Equal(t, 200, r.push(batch(25), nil).Code)

	// a per-key RateLimit overrides the default
	require.NoError(t, r.g.Model(&models.APIKey{}).Where("1=1").Update("rate_limit", 1000).Error)
	r.now = r.now.Add(10 * time.Second)
	require.Equal(t, 200, r.push(batch(500), nil).Code)
}

func TestStreamCardinalityLimit(t *testing.T) {
	r := newRig(t)
	r.h.Limits.MaxStreamsPerKey = 2
	body := line(r.now, "a", "1") + line(r.now, "b", "2") + line(r.now, "c", "3") + line(r.now, "c", "4") + line(r.now, "a", "5")
	rec := r.push(body, nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var resp pushResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 3, resp.Accepted)
	require.Equal(t, 2, resp.Rejected)
	require.Contains(t, resp.Errors[0], "request IDs")
	require.Contains(t, resp.Errors[0], "line 3")
	require.Contains(t, resp.Errors[1], "line 4")
}
