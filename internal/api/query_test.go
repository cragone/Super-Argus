package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/index"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/cragone/Super-Argus/internal/query"
	"github.com/stretchr/testify/require"
)

func newQueryEnv(t *testing.T) (*env, int64) {
	t.Helper()
	e := newEnv(t)
	store := chunk.NewLocalStore(filepath.Join(t.TempDir(), "chunks"))
	ix := index.New(e.srv.DB)
	id, err := ix.Resolve("k", map[string]string{"service": "api", "env": "prod"}, 0)
	require.NoError(t, err)
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).UnixNano()
	var es []chunk.Entry
	for i := 0; i < 10; i++ {
		es = append(es, chunk.Entry{TS: t0 + int64(i)*int64(time.Second), Line: fmt.Sprintf(`{"level":"info","n":%d}`, i)})
	}
	data, meta, err := chunk.Encode(id, es)
	require.NoError(t, err)
	key := chunk.Key(meta.MinTS, id, "c1")
	require.NoError(t, store.Put(key, data))
	require.NoError(t, ix.AddChunk(&models.Chunk{ID: "c1", StreamID: id, Path: key, MinTS: meta.MinTS, MaxTS: meta.MaxTS, LineCount: 10}))
	e.srv.Query = query.NewService(e.srv.DB, store, nil, query.DefaultLimits())
	e.h = NewRouter(e.srv)
	return e, t0
}

func TestQueryRangeAPI(t *testing.T) {
	e, t0 := newQueryEnv(t)
	tok := e.userToken(models.RoleViewer)
	q := func(params url.Values) *struct {
		code int
		body []byte
	} {
		rec := e.do("GET", "/api/v1/query/range?"+params.Encode(), tok, nil)
		return &struct {
			code int
			body []byte
		}{rec.Code, rec.Body.Bytes()}
	}
	p := url.Values{"query": {`{service="api"} | json | n>=7`}, "start": {strconv.FormatInt(t0, 10)},
		"end": {time.Unix(0, t0).Add(time.Minute).UTC().Format(time.RFC3339)}}
	r := q(p)
	require.Equal(t, 200, r.code, string(r.body))
	var out rangeResponse
	require.NoError(t, json.Unmarshal(r.body, &out))
	require.Len(t, out.Entries, 3)
	require.Equal(t, `{"level":"info","n":9}`, out.Entries[0].Line, "backward by default")
	require.Equal(t, "9", out.Entries[0].Fields["n"])
	require.Equal(t, strconv.FormatInt(t0+9*int64(time.Second), 10), out.Entries[0].TS)
	require.Equal(t, "prod", out.Streams[out.Entries[0].Stream]["env"])

	p.Set("direction", "forward")
	p.Set("limit", "1")
	require.NoError(t, json.Unmarshal(q(p).body, &out))
	require.Len(t, out.Entries, 1)
	require.Contains(t, out.Entries[0].Line, `"n":7`)

	// no matches -> empty list, not null
	p.Set("query", `{service="nope"}`)
	r = q(p)
	require.Equal(t, 200, r.code)
	require.Contains(t, string(r.body), `"entries":[]`)

	for name, bad := range map[string]url.Values{
		"bad query":     {"query": {`{`}, "start": p["start"], "end": p["end"]},
		"missing range": {"query": {`{service="api"}`}},
		"bad start":     {"query": {`{service="api"}`}, "start": {"yesterday"}, "end": p["end"]},
		"bad direction": {"query": {`{service="api"}`}, "start": p["start"], "end": p["end"], "direction": {"sideways"}},
		"bad limit":     {"query": {`{service="api"}`}, "start": p["start"], "end": p["end"], "limit": {"-1"}},
	} {
		require.Equal(t, 400, q(bad).code, name)
	}
	tooWide := url.Values{"query": {`{service="api"}`}, "start": {"1"}, "end": {strconv.FormatInt(time.Now().UnixNano(), 10)}}
	require.Equal(t, http.StatusUnprocessableEntity, q(tooWide).code)

	require.Equal(t, 401, e.do("GET", "/api/v1/query/range?"+p.Encode(), "", nil).Code)
}

func TestLabelsAPI(t *testing.T) {
	e, _ := newQueryEnv(t)
	tok := e.userToken(models.RoleViewer)
	rec := e.do("GET", "/api/v1/labels", tok, nil)
	require.Equal(t, 200, rec.Code)
	require.JSONEq(t, `{"data":["env","service"]}`, rec.Body.String())
	rec = e.do("GET", "/api/v1/labels/env/values", tok, nil)
	require.JSONEq(t, `{"data":["prod"]}`, rec.Body.String())
	rec = e.do("GET", "/api/v1/labels/nope/values", tok, nil)
	require.JSONEq(t, `{"data":[]}`, rec.Body.String())
	require.Equal(t, 401, e.do("GET", "/api/v1/labels", "", nil).Code)
	require.Equal(t, 400, e.do("GET", "/api/v1/labels?start=zzz", tok, nil).Code)
}
