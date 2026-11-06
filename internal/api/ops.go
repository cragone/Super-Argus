package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/cragone/Super-Argus/internal/models"
)

// ReadyCheck is one named readiness probe for GET /readyz.
type ReadyCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// readyz reports whether the server can take traffic. It is unauthenticated
// like /healthz and says only which check failed, never why.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	checks := map[string]string{}
	ok := true
	if sqlDB, err := s.DB.DB(); err != nil || sqlDB.PingContext(ctx) != nil {
		checks["database"] = "failed"
		ok = false
	} else {
		checks["database"] = "ok"
	}
	for _, c := range s.ReadyChecks {
		if c.Check(ctx) != nil {
			checks[c.Name] = "failed"
			ok = false
		} else {
			checks[c.Name] = "ok"
		}
	}
	status, word := http.StatusOK, "ready"
	if !ok {
		status, word = http.StatusServiceUnavailable, "unavailable"
	}
	writeJSON(w, status, map[string]any{"status": word, "checks": checks})
}

// metricsHandler serves the Prometheus exposition. A scraper may present
// MetricsToken as a bearer token; otherwise an admin session is required.
func (s *Server) metricsHandler() http.Handler {
	serve := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		s.Metrics.Registry.Write(w)
	})
	admin := s.Authenticate(RequireRole(models.RoleAdmin)(serve))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.MetricsToken != "" {
			if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok &&
				subtle.ConstantTimeCompare([]byte(tok), []byte(s.MetricsToken)) == 1 {
				serve.ServeHTTP(w, r)
				return
			}
		}
		admin.ServeHTTP(w, r)
	})
}
