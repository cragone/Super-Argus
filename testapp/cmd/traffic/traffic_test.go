package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"math/rand/v2"

	"github.com/stretchr/testify/require"
)

func TestPickCoversEveryRouteByWeight(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	seen := map[string]int{}
	for i := 0; i < 20000; i++ {
		rt := pick(r)
		path, _ := rt.build(r)
		p := path
		if i := strings.IndexByte(p, '?'); i >= 0 {
			p = p[:i]
		}
		if strings.HasPrefix(p, "/items/") {
			p = "/items/:id"
		}
		seen[rt.method+" "+p]++
	}
	for _, want := range []string{"GET /items", "POST /items", "GET /items/:id", "DELETE /items/:id", "GET /slow", "GET /flaky", "GET /error", "GET /panic", "GET /noise", "POST /seed", "GET /healthz"} {
		require.Positive(t, seen[want], want)
	}
	require.Greater(t, seen["GET /items"], seen["GET /panic"]*10, "weights are respected")
}

func TestInvalidItemRequestIsReallyInvalid(t *testing.T) {
	var found bool
	r := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 2000 && !found; i++ {
		rt := pick(r)
		if _, body := rt.build(r); strings.Contains(body, `"qty":-1`) {
			found = true
		}
	}
	require.True(t, found)
}

// fakeArgus implements just enough of the Argus API for SetupDashboard.
type fakeArgus struct {
	mu         sync.Mutex
	dashboards []map[string]string
	panels     []map[string]any
	authSeen   []string
	role       string
}

func (f *fakeArgus) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["password"] != "pw" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_credentials","message":"invalid email or password"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	})
	mux.HandleFunc("GET /api/v1/dashboards", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.authSeen = append(f.authSeen, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(f.dashboards)
	})
	mux.HandleFunc("POST /api/v1/dashboards", func(w http.ResponseWriter, r *http.Request) {
		if f.role == "viewer" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"insufficient role"}}`))
			return
		}
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.dashboards = append(f.dashboards, map[string]string{"id": "d1", "title": in["title"]})
		f.mu.Unlock()
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"d1"}`))
	})
	mux.HandleFunc("POST /api/v1/dashboards/d1/panels", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.panels = append(f.panels, in)
		f.mu.Unlock()
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{}`))
	})
	return mux
}

func TestSetupDashboardCreatesPanelsOnceAndIsIdempotent(t *testing.T) {
	f := &fakeArgus{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	id, created, err := SetupDashboard(context.Background(), srv.Client(), srv.URL+"/", "a@b.io", "pw")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "d1", id)
	require.Len(t, f.panels, len(Panels))
	require.Equal(t, []string{"Bearer tok"}, f.authSeen)
	for i, p := range f.panels {
		require.EqualValues(t, i, p["position"])
		require.NotEmpty(t, p["query"])
		require.Contains(t, p["query"], `service="testapp"`)
	}

	id, created, err = SetupDashboard(context.Background(), srv.Client(), srv.URL, "a@b.io", "pw")
	require.NoError(t, err)
	require.False(t, created, "second run reuses the dashboard")
	require.Equal(t, "d1", id)
	require.Len(t, f.panels, len(Panels), "no duplicate panels")
}

func TestSetupDashboardErrors(t *testing.T) {
	f := &fakeArgus{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	_, _, err := SetupDashboard(context.Background(), srv.Client(), srv.URL, "a@b.io", "wrong")
	require.ErrorContains(t, err, "invalid email or password")

	f.role = "viewer"
	_, _, err = SetupDashboard(context.Background(), srv.Client(), srv.URL, "a@b.io", "pw")
	require.ErrorContains(t, err, "editor or admin")
	require.ErrorContains(t, err, "insufficient role")

	_, _, err = SetupDashboard(context.Background(), http.DefaultClient, "http://127.0.0.1:1", "a@b.io", "pw")
	require.Error(t, err, "unreachable server")
}

func TestPanelQueryKinds(t *testing.T) {
	// Guard against typos: every panel needs a metric query except the logs panel.
	for _, p := range Panels {
		switch p.Type {
		case "logs":
			require.True(t, strings.HasPrefix(p.Query, "{"), p.Title)
		case "timeseries", "top_n", "stat":
			require.False(t, strings.HasPrefix(p.Query, "{"), p.Title)
		default:
			t.Fatalf("unknown panel type %q", p.Type)
		}
	}
}
