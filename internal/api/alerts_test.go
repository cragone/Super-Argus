package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cragone/Super-Argus/internal/models"
	"github.com/cragone/Super-Argus/internal/notify"
	"github.com/stretchr/testify/require"
)

func decodeBody[T any](t *testing.T, body []byte) T {
	var v T
	require.NoError(t, json.Unmarshal(body, &v), string(body))
	return v
}

func TestChannelsAndRulesAPI(t *testing.T) {
	e := newEnv(t)
	e.srv.Sealer = notify.NewSealer("test-secret")
	e.srv.Notifier = &notify.Webhook{Sealer: e.srv.Sealer, Attempts: 1}
	e.h = NewRouter(e.srv)
	admin, editor, viewer := e.userToken(models.RoleAdmin), e.userToken(models.RoleEditor), e.userToken(models.RoleViewer)

	var got []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("X-Argus-Signature"))
	}))
	defer hook.Close()

	// Channels: admin writes, editor reads, viewer is locked out.
	require.Equal(t, 403, e.do("POST", "/api/v1/channels", editor, map[string]any{"name": "x", "url": hook.URL}).Code)
	require.Equal(t, 400, e.do("POST", "/api/v1/channels", admin, map[string]any{"name": "x", "url": "nope"}).Code)
	rec := e.do("POST", "/api/v1/channels", admin, map[string]any{"name": "ops", "url": hook.URL + "/h?token=abc", "secret": "s3"})
	require.Equal(t, 201, rec.Code, rec.Body.String())
	ch := decodeBody[channelView](t, rec.Body.Bytes())
	require.True(t, ch.HasSecret)
	require.NotContains(t, rec.Body.String(), "token=abc", "full URL never leaves the server")
	require.NotContains(t, rec.Body.String(), "s3")
	require.Equal(t, 200, e.do("GET", "/api/v1/channels", editor, nil).Code)
	require.Equal(t, 403, e.do("GET", "/api/v1/channels", viewer, nil).Code)

	var raw models.NotificationChannel
	require.NoError(t, e.srv.DB.First(&raw, "id = ?", ch.ID).Error)
	require.NotContains(t, raw.Config, "s3", "secret is stored encrypted")

	rec = e.do("POST", "/api/v1/channels/"+ch.ID+"/test", admin, nil)
	require.Equal(t, 200, rec.Code)
	require.JSONEq(t, `{"ok":true}`, rec.Body.String())
	require.Len(t, got, 1)
	require.Contains(t, got[0], "sha256=")

	// Update keeps the secret when omitted.
	rec = e.do("PUT", "/api/v1/channels/"+ch.ID, admin, map[string]any{"name": "ops2"})
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.True(t, decodeBody[channelView](t, rec.Body.Bytes()).HasSecret)

	// Rules.
	rule := map[string]any{
		"name": "too many errors", "query": `count_over_time({service="api"}[5m])`,
		"comparator": "gt", "threshold": 10, "for_duration": "2m", "severity": "critical",
		"notify_channel_ids": []string{ch.ID},
	}
	require.Equal(t, 403, e.do("POST", "/api/v1/alert-rules", viewer, rule).Code)
	for name, mut := range map[string]func(map[string]any){
		"log query":   func(m map[string]any) { m["query"] = `{service="api"}` },
		"bad cmp":     func(m map[string]any) { m["comparator"] = "approx" },
		"tiny window": func(m map[string]any) { m["window"] = "1s" },
		"bad channel": func(m map[string]any) { m["notify_channel_ids"] = []string{"nope"} },
	} {
		bad := map[string]any{}
		for k, v := range rule {
			bad[k] = v
		}
		mut(bad)
		require.Equal(t, 400, e.do("POST", "/api/v1/alert-rules", editor, bad).Code, name)
	}
	rec = e.do("POST", "/api/v1/alert-rules", editor, rule)
	require.Equal(t, 201, rec.Code, rec.Body.String())
	ar := decodeBody[alertRuleView](t, rec.Body.Bytes())
	require.True(t, ar.Enabled)
	require.Equal(t, "5m", ar.Window)
	require.Equal(t, models.AlertOK, ar.State.State)
	require.Equal(t, []string{ch.ID}, ar.NotifyChannelIDs)

	rule["enabled"] = false
	rec = e.do("PUT", "/api/v1/alert-rules/"+ar.ID, editor, rule)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.False(t, decodeBody[alertRuleView](t, rec.Body.Bytes()).Enabled)

	rec = e.do("GET", "/api/v1/alert-rules", viewer, nil)
	require.Equal(t, 200, rec.Code)
	require.Len(t, decodeBody[[]alertRuleView](t, rec.Body.Bytes()), 1)

	// Events list resolves rule names and honours rule_id; limit is validated.
	require.NoError(t, e.srv.DB.Create(&models.AlertEvent{RuleID: ar.ID, State: "firing", Value: 12, At: e.now}).Error)
	rec = e.do("GET", "/api/v1/alert-events?rule_id="+ar.ID, viewer, nil)
	evs := decodeBody[[]alertEventView](t, rec.Body.Bytes())
	require.Len(t, evs, 1)
	require.Equal(t, "too many errors", evs[0].RuleName)
	require.Equal(t, 400, e.do("GET", "/api/v1/alert-events?limit=0", viewer, nil).Code)

	// Deleting a rule removes its history.
	require.Equal(t, 204, e.do("DELETE", "/api/v1/alert-rules/"+ar.ID, editor, nil).Code)
	require.Equal(t, 404, e.do("GET", "/api/v1/alert-rules/"+ar.ID, viewer, nil).Code)
	var n int64
	e.srv.DB.Model(&models.AlertEvent{}).Count(&n)
	require.Zero(t, n)

	require.Equal(t, 204, e.do("DELETE", "/api/v1/channels/"+ch.ID, admin, nil).Code)
}

func TestChannelTestReportsFailure(t *testing.T) {
	e := newEnv(t)
	e.srv.Sealer = notify.NewSealer("test-secret")
	e.srv.Notifier = &notify.Webhook{Sealer: e.srv.Sealer, Attempts: 1}
	e.h = NewRouter(e.srv)
	admin := e.userToken(models.RoleAdmin)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(400) }))
	defer bad.Close()
	rec := e.do("POST", "/api/v1/channels", admin, map[string]any{"name": "bad", "url": bad.URL})
	ch := decodeBody[channelView](t, rec.Body.Bytes())
	rec = e.do("POST", "/api/v1/channels/"+ch.ID+"/test", admin, nil)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `"ok":false`)
}
