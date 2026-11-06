package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cragone/Super-Argus/internal/metrics"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/stretchr/testify/require"
)

func TestReadyz(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/readyz", "", nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"database":"ok"`)

	e.srv.ReadyChecks = []ReadyCheck{{Name: "wal", Check: func(context.Context) error { return errors.New("disk on fire") }}}
	e.h = NewRouter(e.srv)
	rec = e.do("GET", "/readyz", "", nil)
	require.Equal(t, 503, rec.Code)
	require.Contains(t, rec.Body.String(), `"wal":"failed"`)
	require.NotContains(t, rec.Body.String(), "disk on fire", "reasons stay out of an unauthenticated response")
}

func TestMetricsEndpointAuth(t *testing.T) {
	e := newEnv(t)
	e.srv.Metrics = metrics.NewSet(metrics.NewRegistry())
	e.srv.MetricsToken = "scrape-me"
	e.h = NewRouter(e.srv)
	e.srv.Metrics.Accepted(3, 30)

	get := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, 401, get("").Code)
	require.Equal(t, 401, get("Bearer wrong").Code)
	rec := get("Bearer scrape-me")
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/plain")
	require.Contains(t, rec.Body.String(), "argus_ingest_lines_total 3")

	require.Equal(t, 403, get("Bearer "+e.userToken(models.RoleEditor)).Code, "non-admin sessions are refused")
	require.Equal(t, 200, get("Bearer "+e.userToken(models.RoleAdmin)).Code)

	e.srv.MetricsToken = ""
	e.h = NewRouter(e.srv)
	require.Equal(t, 401, get("Bearer scrape-me").Code, "no token configured means only admin sessions")
}

func TestMetricsDisabledByDefault(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, 404, e.do("GET", "/metrics", "", nil).Code)
}
