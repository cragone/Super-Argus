package api

import (
	"encoding/json"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/models"
	"github.com/stretchr/testify/require"
)

func TestQueryMetricAPI(t *testing.T) {
	e, t0 := newQueryEnv(t) // 10 lines, one per second from t0, stream {service=api, env=prod}
	tok := e.userToken(models.RoleViewer)
	get := func(p url.Values) (int, []byte) {
		rec := e.do("GET", "/api/v1/query/metric?"+p.Encode(), tok, nil)
		return rec.Code, rec.Body.Bytes()
	}
	p := url.Values{
		"query": {`count_over_time({service="api"}[10s])`},
		"start": {strconv.FormatInt(t0, 10)},
		"end":   {strconv.FormatInt(t0+10*int64(time.Second), 10)},
		"step":  {"5s"},
	}
	code, body := get(p)
	require.Equal(t, 200, code, string(body))
	var out metricResponse
	require.NoError(t, json.Unmarshal(body, &out))
	require.Equal(t, int64(5000), out.StepMS)
	require.Len(t, out.Series, 1)
	require.Equal(t, map[string]string{"service": "api", "env": "prod"}, out.Series[0].Labels)
	var vals []float64
	for _, v := range out.Series[0].Values {
		vals = append(vals, v[1])
	}
	require.Equal(t, []float64{1, 6, 9}, vals, "windows (t-10s, t] at t0, t0+5s, t0+10s")
	require.Equal(t, float64(t0/1e6), out.Series[0].Values[0][0], "timestamps are unix milliseconds")

	// seconds as a plain number, automatic step
	p.Set("step", "5")
	code, _ = get(p)
	require.Equal(t, 200, code)
	p.Del("step")
	code, body = get(p)
	require.Equal(t, 200, code, string(body))

	// sum by with an aggregation over the whole stream set
	p.Set("query", `sum by (env) (rate({service="api"}[10s]))`)
	p.Set("step", "10s")
	code, body = get(p)
	require.Equal(t, 200, code, string(body))
	require.Contains(t, string(body), `"env":"prod"`)

	for name, bad := range map[string]url.Values{
		"log query":     {"query": {`{service="api"}`}, "start": p["start"], "end": p["end"]},
		"bad step":      {"query": p["query"], "start": p["start"], "end": p["end"], "step": {"soon"}},
		"missing range": {"query": p["query"]},
		"bad syntax":    {"query": {`rate({service="api"}[5x])`}, "start": p["start"], "end": p["end"]},
	} {
		code, _ = get(bad)
		require.Equal(t, 400, code, name)
	}
	huge := url.Values{"query": p["query"], "start": {"1"}, "end": {strconv.FormatInt(time.Now().UnixNano(), 10)}}
	code, _ = get(huge)
	require.Equal(t, 422, code, "range beyond the metric maximum")

	require.Equal(t, 401, e.do("GET", "/api/v1/query/metric?"+p.Encode(), "", nil).Code)
}

type dash struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Panels []struct {
		ID       string          `json:"id"`
		Title    string          `json:"title"`
		Type     string          `json:"type"`
		Position int             `json:"position"`
		Options  json.RawMessage `json:"options"`
	} `json:"panels"`
	Layout json.RawMessage `json:"layout"`
}

func TestDashboardsCRUDAndRoles(t *testing.T) {
	e := newEnv(t)
	viewer, editor := e.userToken(models.RoleViewer), e.userToken(models.RoleEditor)

	// viewers cannot write
	require.Equal(t, 403, e.do("POST", "/api/v1/dashboards", viewer, map[string]any{"title": "x"}).Code)
	require.Equal(t, 403, e.do("POST", "/api/v1/saved-queries", viewer, map[string]any{"name": "x", "query": `{a="b"}`}).Code)
	require.Equal(t, 401, e.do("GET", "/api/v1/dashboards", "", nil).Code)

	rec := e.do("POST", "/api/v1/dashboards", editor, map[string]any{
		"title": "  API health ", "description": "d", "layout": map[string]any{"columns": 2},
	})
	require.Equal(t, 201, rec.Code, rec.Body.String())
	var d dash
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
	require.Equal(t, "API health", d.Title, "title is trimmed")
	require.JSONEq(t, `{"columns":2}`, string(d.Layout))
	base := "/api/v1/dashboards/" + d.ID

	// panels: all four types, order follows position
	add := func(m map[string]any) int {
		return e.do("POST", base+"/panels", editor, m).Code
	}
	require.Equal(t, 201, add(map[string]any{"title": "Errors", "type": "timeseries", "query": `sum by (level) (count_over_time({service="api"} | json [1m]))`, "step": "30s", "options": map[string]any{"width": "full"}}))
	require.Equal(t, 201, add(map[string]any{"title": "Top routes", "type": "top_n", "query": `topk(5, sum by (route) (rate({service="api"} | json [5m])))`}))
	require.Equal(t, 201, add(map[string]any{"title": "5xx", "type": "stat", "query": `sum(count_over_time({service="api"} | json | status>=500 [1h]))`, "position": -1}))
	require.Equal(t, 201, add(map[string]any{"title": "Recent errors", "type": "logs", "query": `{service="api"} |= "error"`}))

	rec = e.do("GET", base, viewer, nil) // viewers can read
	require.Equal(t, 200, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
	require.Len(t, d.Panels, 4)
	require.Equal(t, "5xx", d.Panels[0].Title, "an explicit lower position sorts first")
	require.JSONEq(t, `{"width":"full"}`, string(d.Panels[1].Options))

	// validation
	for name, m := range map[string]map[string]any{
		"log query in a chart panel": {"title": "t", "type": "timeseries", "query": `{service="api"}`},
		"metric in a logs panel":     {"title": "t", "type": "logs", "query": `rate({service="api"}[1m])`},
		"unknown type":               {"title": "t", "type": "pie", "query": `{a="b"}`},
		"bad syntax":                 {"title": "t", "type": "logs", "query": `{`},
		"no title":                   {"title": " ", "type": "logs", "query": `{a="b"}`},
		"bad step":                   {"title": "t", "type": "timeseries", "query": `rate({a="b"}[1m])`, "step": "100ms"},
		"options not an object":      {"title": "t", "type": "logs", "query": `{a="b"}`, "options": "str"},
	} {
		require.Equal(t, 400, add(m), name)
	}
	require.Equal(t, 404, e.do("POST", "/api/v1/dashboards/nope/panels", editor, map[string]any{"title": "t", "type": "logs", "query": `{a="b"}`}).Code)

	// update a panel and the dashboard
	pid := d.Panels[1].ID
	rec = e.do("PUT", base+"/panels/"+pid, editor, map[string]any{"title": "Renamed", "type": "top_n", "query": `topk(3, rate({service="api"}[5m]))`, "position": 9})
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, 404, e.do("PUT", base+"/panels/nope", editor, map[string]any{"title": "t", "type": "logs", "query": `{a="b"}`}).Code)
	require.Equal(t, 404, e.do("PUT", "/api/v1/dashboards/other/panels/"+pid, editor, map[string]any{"title": "t", "type": "logs", "query": `{a="b"}`}).Code, "panel must belong to the dashboard in the URL")

	rec = e.do("PUT", base, editor, map[string]any{"title": "Renamed board", "layout": []any{1, 2}})
	require.Equal(t, 200, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
	require.Equal(t, "Renamed board", d.Title)
	require.Len(t, d.Panels, 4)
	require.Equal(t, "Renamed", d.Panels[3].Title, "moved to the end by position")
	require.Equal(t, 400, e.do("PUT", base, editor, map[string]any{"title": "x", "layout": "nope"}).Code)

	// list has no panels, delete a panel, delete the dashboard (panels cascade)
	rec = e.do("GET", "/api/v1/dashboards", viewer, nil)
	require.Contains(t, rec.Body.String(), "Renamed board")
	require.Equal(t, 204, e.do("DELETE", base+"/panels/"+pid, editor, nil).Code)
	require.Equal(t, 404, e.do("DELETE", base+"/panels/"+pid, editor, nil).Code)
	require.Equal(t, 204, e.do("DELETE", base, editor, nil).Code)
	require.Equal(t, 404, e.do("GET", base, viewer, nil).Code)
	var n int64
	e.srv.DB.Model(&models.Panel{}).Count(&n)
	require.Zero(t, n, "panels are removed with their dashboard")
	require.Equal(t, 404, e.do("DELETE", base, editor, nil).Code)
}

func TestPanelLimit(t *testing.T) {
	e := newEnv(t)
	editor := e.userToken(models.RoleEditor)
	rec := e.do("POST", "/api/v1/dashboards", editor, map[string]any{"title": "big"})
	var d dash
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
	for i := 0; i < maxPanelsPerD; i++ {
		require.NoError(t, e.srv.DB.Create(&models.Panel{DashboardID: d.ID, Title: "p", Type: "logs", Query: `{a="b"}`, Options: "{}", Position: i}).Error)
	}
	rec = e.do("POST", "/api/v1/dashboards/"+d.ID+"/panels", editor, map[string]any{"title": "one too many", "type": "logs", "query": `{a="b"}`})
	require.Equal(t, 422, rec.Code)
}

func TestSavedQueries(t *testing.T) {
	e := newEnv(t)
	viewer, editor := e.userToken(models.RoleViewer), e.userToken(models.RoleEditor)

	rec := e.do("POST", "/api/v1/saved-queries", editor, map[string]any{"name": "Errors", "query": `{service="api"} |= "error"`})
	require.Equal(t, 201, rec.Code, rec.Body.String())
	var q struct {
		ID           string `json:"id"`
		DefaultRange string `json:"default_range"`
		OwnerID      string `json:"owner_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &q))
	require.Equal(t, "1h", q.DefaultRange, "default range")
	require.NotEmpty(t, q.OwnerID)

	// metric queries can be saved too
	require.Equal(t, 201, e.do("POST", "/api/v1/saved-queries", editor, map[string]any{"name": "Rate", "query": `rate({service="api"}[5m])`, "default_range": "24h"}).Code)
	require.Equal(t, 400, e.do("POST", "/api/v1/saved-queries", editor, map[string]any{"name": "Bad", "query": `{`}).Code)
	require.Equal(t, 400, e.do("POST", "/api/v1/saved-queries", editor, map[string]any{"name": "", "query": `{a="b"}`}).Code)

	rec = e.do("GET", "/api/v1/saved-queries", viewer, nil)
	require.Equal(t, 200, rec.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 2)
	require.Equal(t, "Errors", list[0]["name"], "sorted by name")

	rec = e.do("PUT", "/api/v1/saved-queries/"+q.ID, editor, map[string]any{"name": "Errors v2", "query": `{service="web"}`, "default_range": "6h"})
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "Errors v2")
	require.Equal(t, 404, e.do("PUT", "/api/v1/saved-queries/nope", editor, map[string]any{"name": "x", "query": `{a="b"}`}).Code)
	require.Equal(t, 204, e.do("DELETE", "/api/v1/saved-queries/"+q.ID, editor, nil).Code)
	require.Equal(t, 404, e.do("DELETE", "/api/v1/saved-queries/"+q.ID, editor, nil).Code)
}
