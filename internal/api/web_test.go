package api

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/cragone/Super-Argus/internal/ingest"
	"github.com/cragone/Super-Argus/internal/query"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// The OpenAPI document is the contract the client types are generated from, so
// it must describe exactly the routes the router serves.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(openapiYAML, &spec))
	want := map[string]bool{}
	for p, ops := range spec.Paths {
		for m := range ops {
			want[strings.ToUpper(m)+" /api/v1"+p] = true
		}
	}

	e := newEnv(t)
	e.srv.Ingest, e.srv.Query = &ingest.Handler{}, &query.Service{}
	r := NewRouter(e.srv).(chi.Routes)
	got := map[string]bool{}
	require.NoError(t, chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/v1/") {
			got[method+" "+strings.TrimSuffix(route, "/")] = true
		}
		return nil
	}))
	keys := func(m map[string]bool) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	require.Equal(t, keys(got), keys(want), "router and openapi.yaml disagree")
}

func TestServeOpenAPI(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/api/v1/openapi.yaml", "", nil)
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "application/yaml", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "openapi: 3.0.3")
}

func refreshCookieOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == refreshCookie {
			return c
		}
	}
	return nil
}

func TestRefreshCookieFlow(t *testing.T) {
	e := newEnv(t)
	rec := e.do("POST", "/api/v1/auth/login", "", map[string]string{"email": "admin@x.io", "password": "adminpw"})
	require.Equal(t, 200, rec.Code)
	ck := refreshCookieOf(rec)
	require.NotNil(t, ck)
	require.True(t, ck.HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, ck.SameSite)
	require.Equal(t, "/api/v1/auth", ck.Path)
	require.NotEmpty(t, ck.Value)
	require.False(t, ck.Secure, "plain HTTP in tests")

	// web client: empty body, cookie only
	req := httptest.NewRequest("POST", "/api/v1/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: refreshCookie, Value: ck.Value})
	out := httptest.NewRecorder()
	e.h.ServeHTTP(out, req)
	require.Equal(t, 200, out.Code, out.Body.String())
	require.NotNil(t, refreshCookieOf(out), "cookie is rotated")

	// no body and no cookie
	require.Equal(t, 401, e.do("POST", "/api/v1/auth/refresh", "", nil).Code)
	// garbage cookie
	req = httptest.NewRequest("POST", "/api/v1/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: refreshCookie, Value: "junk"})
	out = httptest.NewRecorder()
	e.h.ServeHTTP(out, req)
	require.Equal(t, 401, out.Code)

	// TLS terminated at a proxy -> Secure cookie
	req = httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"email":"admin@x.io","password":"adminpw"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	out = httptest.NewRecorder()
	e.h.ServeHTTP(out, req)
	require.True(t, refreshCookieOf(out).Secure)

	rec = e.do("POST", "/api/v1/auth/logout", "", nil)
	require.Equal(t, 204, rec.Code)
	require.Equal(t, -1, refreshCookieOf(rec).MaxAge)
}

func TestCORS(t *testing.T) {
	e := newEnv(t)
	e.srv.CORSOrigins = ParseOrigins("http://localhost:8081/, https://argus.example.com")
	h := NewRouter(e.srv)
	do := func(method, origin string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/auth/login", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := do("OPTIONS", "http://localhost:8081", map[string]string{"Access-Control-Request-Method": "POST"})
	require.Equal(t, 204, rec.Code)
	require.Equal(t, "http://localhost:8081", rec.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))
	require.Contains(t, rec.Header().Get("Access-Control-Allow-Headers"), "Authorization")

	rec = do("POST", "https://argus.example.com", nil)
	require.Equal(t, "https://argus.example.com", rec.Header().Get("Access-Control-Allow-Origin"))

	rec = do("POST", "https://evil.example.com", nil)
	require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	require.Empty(t, do("POST", "", nil).Header().Get("Access-Control-Allow-Origin"))

	// no origins configured -> middleware is inert
	e.srv.CORSOrigins = nil
	h = NewRouter(e.srv)
	require.Empty(t, do("POST", "http://localhost:8081", nil).Header().Get("Access-Control-Allow-Origin"))
}

func TestWebFallbackNeverShadowsAPI(t *testing.T) {
	e := newEnv(t)
	e.srv.Web = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("SPA")) })
	h := NewRouter(e.srv)
	get := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		return rec
	}
	require.Equal(t, "SPA", get("/").Body.String())
	require.Equal(t, "SPA", get("/explore").Body.String())
	require.Equal(t, 200, get("/healthz").Code)
	require.Contains(t, get("/healthz").Body.String(), "ok")
	rec := get("/api/v1/nope")
	require.Equal(t, 404, rec.Code)
	require.Contains(t, rec.Body.String(), `"not_found"`)
	require.Equal(t, 200, get("/api/v1/openapi.yaml").Code)
}
