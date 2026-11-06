package ruler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/cragone/Super-Argus/internal/models"
	"github.com/cragone/Super-Argus/internal/notify"
	"github.com/cragone/Super-Argus/internal/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// QueryFunc evaluates a metric query. *query.Service.Metric adapts to it.
type QueryFunc func(ctx context.Context, req query.MetricRequest) (*query.MetricResult, error)

// Ruler evaluates enabled rules every Interval.
type Ruler struct {
	DB       *gorm.DB
	Query    QueryFunc
	Notifier notify.Notifier
	Log      *slog.Logger
	Now      func() time.Time
	Interval time.Duration // default 15s
	// Go runs notification delivery; nil runs it inline. Production sets it to
	// a tracked goroutine launcher so slow retries do not delay evaluation.
	Go func(func())
}

func (r *Ruler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Ruler) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Run ticks until ctx is cancelled.
func (r *Ruler) Run(ctx context.Context) {
	iv := r.Interval
	if iv <= 0 {
		iv = 15 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick evaluates every rule once.
func (r *Ruler) Tick(ctx context.Context) {
	var rules []models.AlertRule
	if err := r.DB.Find(&rules).Error; err != nil {
		r.log().Error("ruler: load rules", "err", err)
		return
	}
	for i := range rules {
		if ctx.Err() != nil {
			return
		}
		r.evalRule(ctx, &rules[i])
	}
}

func (r *Ruler) loadState(rule *models.AlertRule, now time.Time) (models.AlertState, error) {
	st := models.AlertState{RuleID: rule.ID, State: models.AlertOK, Since: now}
	res := r.DB.Where("rule_id = ?", rule.ID).Limit(1).Find(&st)
	return st, res.Error
}

// evaluate runs the rule's query and reduces it to a single value.
func (r *Ruler) evaluate(ctx context.Context, rule *models.AlertRule, now time.Time) (float64, error) {
	win, err := ParseOptDuration(rule.Window)
	if err != nil || win <= 0 {
		return 0, errors.New("invalid window")
	}
	step := max(15*time.Second, (win/100/time.Second)*time.Second)
	res, err := r.Query(ctx, query.MetricRequest{
		Query: rule.Query, Start: now.Add(-win).UnixNano(), End: now.UnixNano(), Step: step,
	})
	if err != nil {
		return 0, err
	}
	return reduce(res, rule.Comparator, rule.Threshold), nil
}

// reduce takes each series' latest point. With no data the value is 0 so that
// "fewer than N" rules fire when logs stop. A breaching series wins; otherwise
// the value closest to breaching is reported.
func reduce(res *query.MetricResult, cmp string, threshold float64) float64 {
	var vals []float64
	for _, s := range res.Series {
		if n := len(s.Points); n > 0 {
			vals = append(vals, s.Points[n-1].V)
		}
	}
	if len(vals) == 0 {
		return 0
	}
	pick := vals[0]
	for _, v := range vals[1:] {
		switch cmp {
		case "lt", "lte":
			if v < pick {
				pick = v
			}
		case "eq":
			if v == threshold {
				pick = v
			}
		default:
			if v > pick {
				pick = v
			}
		}
	}
	return pick
}

func (r *Ruler) evalRule(ctx context.Context, rule *models.AlertRule) {
	now := r.now()
	st, err := r.loadState(rule, now)
	if err != nil {
		r.log().Error("ruler: load state", "rule", rule.ID, "err", err)
		return
	}
	if !rule.Enabled {
		if st.State != models.AlertOK {
			st.State, st.Since, st.Notified = models.AlertOK, now, false
			r.saveState(&st)
		}
		return
	}
	value, err := r.evaluate(ctx, rule, now)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		st.LastEval, st.LastError = &now, err.Error()
		r.saveState(&st)
		r.log().Warn("ruler: evaluate", "rule", rule.ID, "err", err)
		return
	}
	forDur, _ := ParseOptDuration(rule.ForDuration)
	cooldown, _ := ParseOptDuration(rule.Cooldown)
	tr := Step(st.State, st.Since, now, Breached(rule.Comparator, value, rule.Threshold), forDur)

	st.LastEval, st.LastValue, st.LastError = &now, value, ""
	st.State, st.Since = tr.State, tr.Since

	var send bool
	switch tr.Event {
	case models.AlertFiring:
		inCooldown := st.LastNotifiedAt != nil && now.Sub(*st.LastNotifiedAt) < cooldown
		send = !inCooldown
		st.Notified = send
		if send {
			st.LastNotifiedAt = &now
		}
	case models.AlertResolved:
		send = st.Notified
		st.Notified = false
	default:
		if tr.Changed && tr.State == models.AlertOK { // pending that cleared
			st.Notified = false
		}
	}
	r.saveState(&st)

	if tr.Event == "" {
		return
	}
	ev := models.AlertEvent{RuleID: rule.ID, State: tr.Event, Value: value, At: now}
	if err := r.DB.Create(&ev).Error; err != nil {
		r.log().Error("ruler: record event", "rule", rule.ID, "err", err)
		return
	}
	if send {
		r.dispatch(func() { r.deliver(ctx, rule, ev) })
	}
}

func (r *Ruler) saveState(st *models.AlertState) {
	err := r.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(st).Error
	if err != nil {
		r.log().Error("ruler: save state", "rule", st.RuleID, "err", err)
	}
}

func (r *Ruler) dispatch(f func()) {
	if r.Go != nil {
		r.Go(f)
		return
	}
	f()
}

// Payload builds the webhook body for a rule and event state.
func Payload(rule *models.AlertRule, status string, value float64, at time.Time) notify.Payload {
	return notify.Payload{
		Status: status, RuleID: rule.ID, RuleName: rule.Name, Severity: rule.Severity,
		Query: rule.Query, Comparator: rule.Comparator, Threshold: rule.Threshold, Value: value, At: at,
	}
}

// deliver sends to each of the rule's channels and records the outcome on the event.
func (r *Ruler) deliver(ctx context.Context, rule *models.AlertRule, ev models.AlertEvent) {
	var ids []string
	_ = json.Unmarshal([]byte(rule.NotifyChannelIDs), &ids)
	if len(ids) == 0 || r.Notifier == nil {
		return
	}
	var chans []models.NotificationChannel
	if err := r.DB.Where("id IN ?", ids).Find(&chans).Error; err != nil {
		r.log().Error("ruler: load channels", "rule", rule.ID, "err", err)
		return
	}
	var errMsg string
	sent := false
	for _, ch := range chans {
		if err := r.Notifier.Send(ctx, ch, Payload(rule, ev.State, ev.Value, ev.At)); err != nil {
			r.log().Warn("ruler: notify", "rule", rule.ID, "channel", ch.ID, "err", err)
			errMsg = ch.Name + ": " + err.Error()
			continue
		}
		sent = true
	}
	r.DB.Model(&models.AlertEvent{}).Where("id = ?", ev.ID).
		Updates(map[string]any{"notified": sent, "notify_error": errMsg})
}
