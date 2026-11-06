package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/models"
	"github.com/stretchr/testify/require"
)

func channel(t *testing.T, s *Sealer, url, secret string) models.NotificationChannel {
	cfg := WebhookConfig{URL: url}
	if secret != "" {
		enc, err := s.Seal(secret)
		require.NoError(t, err)
		cfg.SecretEnc = enc
	}
	b, _ := json.Marshal(cfg)
	return models.NotificationChannel{Name: "c", Type: "webhook", Config: string(b)}
}

func TestSealRoundTrip(t *testing.T) {
	s := NewSealer("k")
	enc, err := s.Seal("hunter2")
	require.NoError(t, err)
	require.NotContains(t, enc, "hunter2")
	got, err := s.Open(enc)
	require.NoError(t, err)
	require.Equal(t, "hunter2", got)
	_, err = NewSealer("other").Open(enc)
	require.Error(t, err)
}

func TestWebhookRetriesThenSucceedsAndSigns(t *testing.T) {
	s := NewSealer("k")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("topsecret"))
		mac.Write(body)
		require.Equal(t, "sha256="+hex.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Argus-Signature"))
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	var sleeps []time.Duration
	w := &Webhook{Sealer: s, Sleep: func(d time.Duration) { sleeps = append(sleeps, d) }}
	require.NoError(t, w.Send(t.Context(), channel(t, s, srv.URL, "topsecret"), Payload{Status: "firing"}))
	require.Equal(t, 3, calls)
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second}, sleeps)
}

func TestWebhookGivesUpAndSkipsRetryOn4xx(t *testing.T) {
	s := NewSealer("k")
	calls, status := 0, http.StatusInternalServerError
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(status)
	}))
	defer srv.Close()
	w := &Webhook{Sealer: s, Sleep: func(time.Duration) {}}
	require.Error(t, w.Send(t.Context(), channel(t, s, srv.URL, ""), Payload{}))
	require.Equal(t, 3, calls, "three attempts then give up")

	calls, status = 0, http.StatusBadRequest
	require.Error(t, w.Send(t.Context(), channel(t, s, srv.URL, ""), Payload{}))
	require.Equal(t, 1, calls, "4xx is not retried")
}

func TestValidateURL(t *testing.T) {
	require.NoError(t, ValidateURL("https://example.com/hook"))
	for _, bad := range []string{"", "ftp://x", "example.com", "http://"} {
		require.Error(t, ValidateURL(bad), bad)
	}
}
