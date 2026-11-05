package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/auth"
	"github.com/cragone/Super-Argus/internal/db"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/stretchr/testify/require"
)

type env struct {
	t   *testing.T
	srv *Server
	h   http.Handler
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	g, err := db.Open("sqlite:" + filepath.Join(t.TempDir(), "t.db"))
	require.NoError(t, err)
	sqlDB, _ := g.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Migrate(g))
	require.NoError(t, auth.SeedAdmin(g, "admin@x.io", "adminpw", slog.New(slog.NewTextHandler(io.Discard, nil))))

	e := &env{t: t, now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tm := auth.NewTokenManager("test-secret")
	tm.Now = func() time.Time { return e.now }
	e.srv = &Server{DB: g, Tokens: tm}
	e.h = NewRouter(e.srv)
	return e
}

func (e *env) do(method, path, token string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		require.NoError(e.t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) login(email, pw string) (access, refresh string, code int) {
	rec := e.do("POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": pw})
	var tr tokenResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &tr)
	return tr.AccessToken, tr.RefreshToken, rec.Code
}

func (e *env) userToken(role string) string {
	h, _ := auth.HashPassword("pw")
	u := models.User{Email: "test-" + role + "@x.io", PasswordHash: h, Role: role}
	require.NoError(e.t, e.srv.DB.Create(&u).Error)
	acc, _, code := e.login(u.Email, "pw")
	require.Equal(e.t, 200, code)
	return acc
}

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, 200, e.do("GET", "/healthz", "", nil).Code)
}

func TestLoginAndMe(t *testing.T) {
	e := newEnv(t)
	_, _, code := e.login("admin@x.io", "wrong")
	require.Equal(t, 401, code)
	_, _, code = e.login("nobody@x.io", "adminpw")
	require.Equal(t, 401, code)

	acc, _, code := e.login("admin@x.io", "adminpw")
	require.Equal(t, 200, code)
	rec := e.do("GET", "/api/v1/me", acc, nil)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `"role":"admin"`)
}

func TestRefresh(t *testing.T) {
	e := newEnv(t)
	acc, ref, _ := e.login("admin@x.io", "adminpw")

	require.Equal(t, 401, e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": acc}).Code, "access token is not a refresh token")
	rec := e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": ref})
	require.Equal(t, 200, rec.Code)

	e.now = e.now.Add(8 * 24 * time.Hour)
	require.Equal(t, 401, e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": ref}).Code)
}

func TestAuthMiddleware(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, 401, e.do("GET", "/api/v1/me", "", nil).Code)
	require.Equal(t, 401, e.do("GET", "/api/v1/me", "garbage", nil).Code)

	acc, ref, _ := e.login("admin@x.io", "adminpw")
	require.Equal(t, 401, e.do("GET", "/api/v1/me", ref, nil).Code, "refresh token rejected on API routes")

	e.now = e.now.Add(time.Hour)
	require.Equal(t, 401, e.do("GET", "/api/v1/me", acc, nil).Code, "expired access token")
}

func TestRoleEnforcement(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		role string
		want int
	}{{models.RoleViewer, 403}, {models.RoleEditor, 403}, {models.RoleAdmin, 200}} {
		tok := e.userToken(tc.role)
		require.Equal(t, 200, e.do("GET", "/api/v1/me", tok, nil).Code, tc.role)
		require.Equal(t, tc.want, e.do("GET", "/api/v1/api-keys", tok, nil).Code, tc.role)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	e := newEnv(t)
	admin, _, _ := e.login("admin@x.io", "adminpw")

	require.Equal(t, 400, e.do("POST", "/api/v1/api-keys", admin, map[string]string{}).Code)

	rec := e.do("POST", "/api/v1/api-keys", admin, map[string]any{"name": "ci", "rate_limit": 100})
	require.Equal(t, 201, rec.Code)
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.Key)

	// Plaintext key never stored or listed again.
	var stored models.APIKey
	require.NoError(t, e.srv.DB.First(&stored, "id = ?", created.ID).Error)
	require.NotEqual(t, created.Key, stored.KeyHash)
	list := e.do("GET", "/api/v1/api-keys", admin, nil)
	require.NotContains(t, list.Body.String(), created.Key)

	_, err := auth.LookupAPIKey(e.srv.DB, created.Key)
	require.NoError(t, err)

	require.Equal(t, 204, e.do("DELETE", "/api/v1/api-keys/"+created.ID, admin, nil).Code)
	require.Equal(t, 404, e.do("DELETE", "/api/v1/api-keys/"+created.ID, admin, nil).Code)
	_, err = auth.LookupAPIKey(e.srv.DB, created.Key)
	require.ErrorIs(t, err, auth.ErrInvalidKey)
}
