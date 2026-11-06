// Package notify delivers alert notifications to channels.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/cragone/Super-Argus/internal/models"
)

// WebhookConfig is the JSON stored in NotificationChannel.Config for type
// "webhook". SecretEnc is the Sealer-encrypted signing secret (optional).
type WebhookConfig struct {
	URL       string `json:"url"`
	SecretEnc string `json:"secret_enc,omitempty"`
}

// Payload is the JSON body POSTed to a webhook.
type Payload struct {
	Status     string    `json:"status"` // firing | resolved | test
	RuleID     string    `json:"rule_id"`
	RuleName   string    `json:"rule_name"`
	Severity   string    `json:"severity"`
	Query      string    `json:"query"`
	Comparator string    `json:"comparator"`
	Threshold  float64   `json:"threshold"`
	Value      float64   `json:"value"`
	At         time.Time `json:"at"`
}

// Notifier sends a payload to a channel.
type Notifier interface {
	Send(ctx context.Context, ch models.NotificationChannel, p Payload) error
}

// Webhook delivers payloads over HTTP with retry and backoff. 5xx, 429 and
// network errors are retried; other 4xx responses fail immediately.
type Webhook struct {
	Client   *http.Client
	Sealer   *Sealer
	Attempts int                 // default 3
	Backoff  time.Duration       // first delay, doubled each retry; default 1s
	Sleep    func(time.Duration) // injectable for tests
}

// ValidateURL checks that raw is an absolute http(s) URL.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be an absolute http or https URL")
	}
	return nil
}

// Send implements Notifier.
func (w *Webhook) Send(ctx context.Context, ch models.NotificationChannel, p Payload) error {
	var cfg WebhookConfig
	if err := json.Unmarshal([]byte(ch.Config), &cfg); err != nil || cfg.URL == "" {
		return errors.New("channel has no valid webhook config")
	}
	var secret string
	if cfg.SecretEnc != "" && w.Sealer != nil {
		s, err := w.Sealer.Open(cfg.SecretEnc)
		if err != nil {
			return errors.New("cannot decrypt channel secret")
		}
		secret = s
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	attempts, backoff, sleep := w.Attempts, w.Backoff, w.Sleep
	if attempts <= 0 {
		attempts = 3
	}
	if backoff <= 0 {
		backoff = time.Second
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			sleep(backoff << (i - 1))
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		retry, err := w.post(ctx, client, cfg.URL, secret, body)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return lastErr
}

func (w *Webhook) post(ctx context.Context, c *http.Client, target, secret string, body []byte) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "argus-alerts")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Argus-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := c.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode/100 == 2:
		return false, nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return true, fmt.Errorf("webhook returned %d", resp.StatusCode)
	default:
		return false, fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
}
