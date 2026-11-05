// Package api contains the HTTP router, middleware and handlers.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/cragone/Super-Argus/internal/auth"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"gorm.io/gorm"
)

// Server holds handler dependencies.
type Server struct {
	DB     *gorm.DB
	Tokens *auth.TokenManager
}

// NewRouter builds the HTTP router.
func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/login", s.login)
		r.Post("/auth/refresh", s.refresh)

		r.Group(func(r chi.Router) {
			r.Use(s.Authenticate)
			r.With(RequireRole(models.RoleViewer)).Get("/me", s.me)

			r.Group(func(r chi.Router) {
				r.Use(RequireRole(models.RoleAdmin))
				r.Get("/api-keys", s.listAPIKeys)
				r.Post("/api-keys", s.createAPIKey)
				r.Delete("/api-keys/{id}", s.revokeAPIKey)
			})
		})
	})
	return r
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{errorDetail{code, msg}})
}
