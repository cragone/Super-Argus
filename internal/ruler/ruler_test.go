package ruler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/db"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/cragone/Super-Argus/internal/notify"
	"github.com/cragone/Super-Argus/internal/query"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestStep(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	min := time.Minute
	cases := []struct {
		name     string
		cur      string
		since    time.Time
		now      time.Time
		breached bool
		forDur   time.Duration
		state    string
		event    string
	}{
		{"ok stays ok", models.AlertOK, t0, t0.Add(min), false, 0, models.AlertOK, ""},
		{"ok breach no for fires", models.AlertOK, t0, t0.Add(min), true, 0, models.AlertFiring, models.AlertFiring},
		{"ok breach with for pends", models.AlertOK, t0, t0.Add(min), true, 5 * min, models.AlertPending, models.AlertPending},
		{"pending before for stays", models.AlertPending, t0, t0.Add(4 * min), true, 5 * min, models.AlertPending, ""},
		{"pending at for fires", models.AlertPending, t0, t0.Add(5 * min), true, 5 * min, models.AlertFiring, models.AlertFiring},
		{"pending clears silently", models.AlertPending, t0, t0.Add(min), false, 5 * min, models.AlertOK, ""},
		{"firing stays", models.AlertFiring, t0, t0.Add(min), true, 5 * min, models.AlertFiring, ""},
		{"firing resolves", models.AlertFiring, t0, t0.Add(min), false, 5 * min, models.AlertOK, models.AlertResolved},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := Step(c.cur, c.since, c.now, c.breached, c.forDur)
			require.Equal(t, c.state, tr.State)
			require.Equal(t, c.event, tr.Event)
		})
	}
}

func TestBreached(t *testing.T) {
	require.True(t, Breached("gt", 2, 1))
	require.False(t, Breached("gt", 1, 1))
	require.True(t, Breached("gte", 1, 1))
	require.True(t, Breached("lt", 0, 1))
	require.True(t, Breached("lte", 1, 1))
	require.True(t, Breached("eq", 3, 3))
	require.False(t, Breached("nope", 3, 3))
}

type harness struct {
	db    *gorm.DB
	clock time.Time
	value float64
	mu    sync.Mutex
	hooks []notify.Payload
	srv   *httptest.Server
	rule  models.AlertRule
	path  string
}

func newHarness(t *testing.T, forDur, cooldown string) *harness {
	t.Helper()
	h := &harness{clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	h.path = filepath.Join(t.TempDir(), "t.db")
	h.db = h.open(t)
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p notify.Payload
		_ = json.NewDecoder(r.Body).Decode(&p)
		h.mu.Lock()
		h.hooks = append(h.hooks, p)
		h.mu.Unlock()
	}))
	t.Cleanup(h.srv.Close)
	cfg, _ := json.Marshal(notify.WebhookConfig{URL: h.srv.URL})
	ch := models.NotificationChannel{Name: "hook", Type: "webhook", Config: string(cfg)}
	require.NoError(t, h.db.Create(&ch).Error)
	ids, _ := json.Marshal([]string{ch.ID})
	h.rule = models.AlertRule{
		OwnerID: "u", Name: "errors", Query: `count_over_time({service="api"}[5m])`, Comparator: "gt", Threshold: 10,
		Window: "5m", ForDuration: forDur, Cooldown: cooldown, Severity: "critical", Enabled: true, NotifyChannelIDs: string(ids),
	}
	require.NoError(t, Validate(&h.rule))
	require.NoError(t, h.db.Create(&h.rule).Error)
	return h
}

func (h *harness) open(t *testing.T) *gorm.DB {
	g, err := db.Open("sqlite:" + h.path)
	require.NoError(t, err)
	require.NoError(t, db.Migrate(g))
	sqlDB, _ := g.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	return g
}

// ruler builds a fresh Ruler (as after a restart) over the harness database.
func (h *harness) ruler() *Ruler {
	return &Ruler{
		DB: h.db, Notifier: &notify.Webhook{Sleep: func(time.Duration) {}},
		Now: func() time.Time { return h.clock },
		Query: func(_ context.Context, _ query.MetricRequest) (*query.MetricResult, error) {
			return &query.MetricResult{Series: []query.Series{{Points: []query.Point{{T: 1, V: h.value}}}}}, nil
		},
	}
}

func (h *harness) statuses() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, p := range h.hooks {
		out = append(out, p.Status)
	}
	return out
}

func (h *harness) tick(r *Ruler, advance time.Duration, value float64) {
	h.clock = h.clock.Add(advance)
	h.value = value
	r.Tick(context.Background())
}

func TestFireResolveAndRestart(t *testing.T) {
	h := newHarness(t, "2m", "0s")
	r := h.ruler()

	h.tick(r, 0, 5) // below threshold
	require.Empty(t, h.statuses())

	h.tick(r, 15*time.Second, 50) // breach -> pending
	h.tick(r, 60*time.Second, 50) // 60s < 2m
	require.Empty(t, h.statuses(), "no webhook while pending")

	h.tick(r, 60*time.Second, 50) // 2m reached -> firing
	require.Equal(t, []string{"firing"}, h.statuses())

	r2 := h.ruler() // simulated restart: persisted state must prevent re-fire
	h.tick(r2, 15*time.Second, 50)
	h.tick(r2, 15*time.Second, 50)
	require.Equal(t, []string{"firing"}, h.statuses(), "exactly one webhook across restart")

	h.tick(r2, 15*time.Second, 1) // clears
	require.Equal(t, []string{"firing", "resolved"}, h.statuses())

	var evs []models.AlertEvent
	require.NoError(t, h.db.Order("at").Find(&evs).Error)
	var states []string
	for _, e := range evs {
		states = append(states, e.State)
	}
	require.Equal(t, []string{"pending", "firing", "resolved"}, states)
	require.True(t, evs[1].Notified)

	h.mu.Lock()
	p := h.hooks[0]
	h.mu.Unlock()
	require.Equal(t, "errors", p.RuleName)
	require.Equal(t, "critical", p.Severity)
	require.Equal(t, float64(50), p.Value)
}

func TestCooldownSuppressesRepeatNotifications(t *testing.T) {
	h := newHarness(t, "0s", "10m")
	r := h.ruler()
	h.tick(r, 0, 50) // fires + notifies
	h.tick(r, time.Minute, 1)
	require.Equal(t, []string{"firing", "resolved"}, h.statuses())
	h.tick(r, time.Minute, 50) // fires again inside cooldown: recorded, not sent
	h.tick(r, time.Minute, 1)  // resolve of an unnotified firing: not sent
	require.Equal(t, []string{"firing", "resolved"}, h.statuses())

	var n int64
	h.db.Model(&models.AlertEvent{}).Where("state = ?", "firing").Count(&n)
	require.EqualValues(t, 2, n, "every transition is still recorded")

	h.tick(r, 20*time.Minute, 50) // outside cooldown
	require.Equal(t, []string{"firing", "resolved", "firing"}, h.statuses())
}

func TestDisabledRuleResetsAndErrorsKeepState(t *testing.T) {
	h := newHarness(t, "0s", "0s")
	r := h.ruler()
	h.tick(r, 0, 50)
	require.Equal(t, []string{"firing"}, h.statuses())

	failing := false
	good := r.Query
	r.Query = func(ctx context.Context, req query.MetricRequest) (*query.MetricResult, error) {
		if failing {
			return nil, context.DeadlineExceeded
		}
		return good(ctx, req)
	}
	failing = true
	h.tick(r, time.Minute, 0)
	var st models.AlertState
	require.NoError(t, h.db.First(&st, "rule_id = ?", h.rule.ID).Error)
	require.Equal(t, models.AlertFiring, st.State, "evaluation errors leave the state alone")
	require.NotEmpty(t, st.LastError)
	failing = false

	require.NoError(t, h.db.Model(&models.AlertRule{}).Where("id = ?", h.rule.ID).Update("enabled", false).Error)
	h.tick(r, time.Minute, 50)
	require.NoError(t, h.db.First(&st, "rule_id = ?", h.rule.ID).Error)
	require.Equal(t, models.AlertOK, st.State)
	require.Equal(t, []string{"firing"}, h.statuses())
}

func TestReduce(t *testing.T) {
	res := &query.MetricResult{Series: []query.Series{
		{Points: []query.Point{{V: 1}, {V: 3}}}, {Points: []query.Point{{V: 9}}}, {},
	}}
	require.Equal(t, float64(9), reduce(res, "gt", 0))
	require.Equal(t, float64(3), reduce(res, "lt", 0))
	require.Equal(t, float64(0), reduce(&query.MetricResult{}, "gt", 0), "no data reads as zero")
}

func TestValidate(t *testing.T) {
	ok := models.AlertRule{Query: `rate({a="b"}[1m])`, Comparator: "gt", Window: "5m", ForDuration: "0s", Cooldown: "", Severity: "info"}
	require.NoError(t, Validate(&ok))
	for name, mut := range map[string]func(*models.AlertRule){
		"log query":  func(r *models.AlertRule) { r.Query = `{a="b"}` },
		"comparator": func(r *models.AlertRule) { r.Comparator = "~" },
		"tiny win":   func(r *models.AlertRule) { r.Window = "1s" },
		"huge win":   func(r *models.AlertRule) { r.Window = "2h" },
		"bad for":    func(r *models.AlertRule) { r.ForDuration = "soon" },
		"severity":   func(r *models.AlertRule) { r.Severity = "meh" },
	} {
		r := ok
		mut(&r)
		require.Error(t, Validate(&r), name)
	}
}
