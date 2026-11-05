package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cragone/Super-Argus/internal/auth"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

const refreshCookie = "argus_refresh"

// setRefreshCookie lets the web client keep its refresh token out of JavaScript.
func (s *Server) setRefreshCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookie, Value: token, Path: "/api/v1/auth", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", MaxAge: maxAge,
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.setRefreshCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) tokenPair(w http.ResponseWriter, r *http.Request, u *models.User) {
	acc, ref, err := s.Tokens.IssuePair(u.ID, u.Role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not issue token")
		return
	}
	s.setRefreshCookie(w, r, ref, int(s.Tokens.RefreshTTL.Seconds()))
	writeJSON(w, http.StatusOK, tokenResponse{acc, ref, int(s.Tokens.AccessTTL.Seconds())})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

// dummyHash equalizes timing for unknown emails.
var dummyHash, _ = auth.HashPassword("argus-dummy-password")

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	var u models.User
	err := s.DB.Where("email = ?", strings.ToLower(strings.TrimSpace(in.Email))).First(&u).Error
	if err != nil {
		auth.VerifyPassword(in.Password, dummyHash)
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(w, http.StatusInternalServerError, "internal", "lookup failed")
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	if !auth.VerifyPassword(in.Password, u.PasswordHash) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	s.tokenPair(w, r, &u)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	// The token may come from the body (native clients) or the HttpOnly cookie (web).
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if in.RefreshToken == "" {
		if ck, err := r.Cookie(refreshCookie); err == nil {
			in.RefreshToken = ck.Value
		}
	}
	c, err := s.Tokens.Parse(in.RefreshToken, auth.TypeRefresh)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired refresh token")
		return
	}
	var u models.User
	if err := s.DB.First(&u, "id = ?", c.Subject).Error; err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "user no longer exists")
		return
	}
	s.tokenPair(w, r, &u)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	var u models.User
	if err := s.DB.First(&u, "id = ?", p.UserID).Error; err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "user no longer exists")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": u.ID, "email": u.Email, "role": u.Role})
}

type apiKeyView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Label      string     `json:"label"`
	RateLimit  int        `json:"rate_limit"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

func viewKey(k models.APIKey) apiKeyView {
	return apiKeyView{k.ID, k.Name, k.KeyPrefix, k.Label, k.RateLimit, k.LastUsedAt, k.RevokedAt, k.CreatedAt}
}

func (s *Server) listAPIKeys(w http.ResponseWriter, _ *http.Request) {
	var ks []models.APIKey
	if err := s.DB.Order("created_at desc").Find(&ks).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	out := make([]apiKeyView, 0, len(ks))
	for _, k := range ks {
		out = append(out, viewKey(k))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string         `json:"name"`
		Label         string         `json:"label"`
		RateLimit     int            `json:"rate_limit"`
		AllowedLabels map[string]any `json:"allowed_labels"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	key, prefix, hash, err := auth.GenerateAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "key generation failed")
		return
	}
	allowed := "{}"
	if in.AllowedLabels != nil {
		b, _ := json.Marshal(in.AllowedLabels)
		allowed = string(b)
	}
	k := models.APIKey{Name: in.Name, KeyHash: hash, KeyPrefix: prefix, Label: in.Label, AllowedLabels: allowed, RateLimit: in.RateLimit}
	if err := s.DB.Create(&k).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not store key")
		return
	}
	// The plaintext key is shown exactly once.
	writeJSON(w, http.StatusCreated, struct {
		apiKeyView
		Key string `json:"key"`
	}{viewKey(k), key})
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	res := s.DB.Model(&models.APIKey{}).Where("id = ? AND revoked_at IS NULL", chi.URLParam(r, "id")).
		Update("revoked_at", time.Now().UTC())
	if res.Error != nil {
		writeError(w, http.StatusInternalServerError, "internal", "revoke failed")
		return
	}
	if res.RowsAffected == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no active key with that id")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
