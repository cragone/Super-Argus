package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cragone/Super-Argus/internal/models"
	"github.com/cragone/Super-Argus/internal/notify"
	"github.com/cragone/Super-Argus/internal/ruler"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

const maxChannelsPerRule = 10

// --- notification channels ---

type channelView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	URLHost   string    `json:"url_host"`
	HasSecret bool      `json:"has_secret"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func viewChannel(c models.NotificationChannel) channelView {
	v := channelView{ID: c.ID, Name: c.Name, Type: c.Type, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	var cfg notify.WebhookConfig
	if json.Unmarshal([]byte(c.Config), &cfg) == nil {
		if u, err := url.Parse(cfg.URL); err == nil {
			v.URLHost = u.Host
		}
		v.HasSecret = cfg.SecretEnc != ""
	}
	return v
}

type channelInput struct {
	Name   string  `json:"name"`
	Type   string  `json:"type"`
	URL    string  `json:"url"`
	Secret *string `json:"secret"` // nil keeps the existing secret on update; "" clears it
}

func (s *Server) listChannels(w http.ResponseWriter, _ *http.Request) {
	var cs []models.NotificationChannel
	if err := s.DB.Order("name").Find(&cs).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	out := make([]channelView, 0, len(cs))
	for _, c := range cs {
		out = append(out, viewChannel(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// buildConfig validates the input and returns the stored config JSON. prev is
// the existing config (empty on create).
func (s *Server) buildConfig(in *channelInput, prev string) (string, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > maxTitle {
		return "", errors.New("name is required (max 200 characters)")
	}
	if in.Type == "" {
		in.Type = "webhook"
	}
	if in.Type != "webhook" {
		return "", errors.New("type must be webhook")
	}
	var cfg notify.WebhookConfig
	if prev != "" {
		_ = json.Unmarshal([]byte(prev), &cfg)
	}
	if in.URL != "" || prev == "" {
		if err := notify.ValidateURL(in.URL); err != nil {
			return "", err
		}
		cfg.URL = in.URL
	}
	if in.Secret != nil {
		if *in.Secret == "" {
			cfg.SecretEnc = ""
		} else {
			if len(*in.Secret) > 512 {
				return "", errors.New("secret is too long")
			}
			if s.Sealer == nil {
				return "", errors.New("secrets are not enabled")
			}
			enc, err := s.Sealer.Seal(*in.Secret)
			if err != nil {
				return "", errors.New("could not encrypt secret")
			}
			cfg.SecretEnc = enc
		}
	}
	b, _ := json.Marshal(cfg)
	return string(b), nil
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if !decode(w, r, &in) {
		return
	}
	cfg, err := s.buildConfig(&in, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	c := models.NotificationChannel{Name: in.Name, Type: in.Type, Config: cfg}
	if err := s.DB.Create(&c).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not save channel")
		return
	}
	writeJSON(w, http.StatusCreated, viewChannel(c))
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if !decode(w, r, &in) {
		return
	}
	var c models.NotificationChannel
	if !s.find(w, &c, chi.URLParam(r, "id")) {
		return
	}
	cfg, err := s.buildConfig(&in, c.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	c.Name, c.Type, c.Config = in.Name, in.Type, cfg
	if err := s.DB.Save(&c).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not save channel")
		return
	}
	writeJSON(w, http.StatusOK, viewChannel(c))
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	s.deleteByID(w, &models.NotificationChannel{}, chi.URLParam(r, "id"))
}

// testChannel sends a sample payload and reports the delivery result.
func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	var c models.NotificationChannel
	if !s.find(w, &c, chi.URLParam(r, "id")) {
		return
	}
	if s.Notifier == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "notifications are disabled")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	sample := notify.Payload{Status: "test", RuleID: "test", RuleName: "Argus test notification",
		Severity: "info", Query: "count_over_time({service=\"demo\"}[5m])", Comparator: "gt",
		Threshold: 0, Value: 1, At: time.Now().UTC()}
	if err := s.Notifier.Send(ctx, c, sample); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- alert rules ---

type alertRuleInput struct {
	Name             string   `json:"name"`
	Query            string   `json:"query"`
	Comparator       string   `json:"comparator"`
	Threshold        float64  `json:"threshold"`
	Window           string   `json:"window"`
	ForDuration      string   `json:"for_duration"`
	Cooldown         string   `json:"cooldown"`
	Severity         string   `json:"severity"`
	Enabled          *bool    `json:"enabled"`
	NotifyChannelIDs []string `json:"notify_channel_ids"`
}

type alertStateView struct {
	State     string     `json:"state"`
	Since     time.Time  `json:"since"`
	LastEval  *time.Time `json:"last_eval"`
	LastValue float64    `json:"last_value"`
	LastError string     `json:"last_error"`
}

type alertRuleView struct {
	ID               string         `json:"id"`
	OwnerID          string         `json:"owner_id"`
	Name             string         `json:"name"`
	Query            string         `json:"query"`
	Comparator       string         `json:"comparator"`
	Threshold        float64        `json:"threshold"`
	Window           string         `json:"window"`
	ForDuration      string         `json:"for_duration"`
	Cooldown         string         `json:"cooldown"`
	Severity         string         `json:"severity"`
	Enabled          bool           `json:"enabled"`
	NotifyChannelIDs []string       `json:"notify_channel_ids"`
	State            alertStateView `json:"state"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

func viewRule(r models.AlertRule, st *models.AlertState) alertRuleView {
	ids := []string{}
	_ = json.Unmarshal([]byte(r.NotifyChannelIDs), &ids)
	v := alertRuleView{
		ID: r.ID, OwnerID: r.OwnerID, Name: r.Name, Query: r.Query, Comparator: r.Comparator,
		Threshold: r.Threshold, Window: r.Window, ForDuration: r.ForDuration, Cooldown: r.Cooldown,
		Severity: r.Severity, Enabled: r.Enabled, NotifyChannelIDs: ids,
		State:     alertStateView{State: models.AlertOK, Since: r.CreatedAt},
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if st != nil {
		v.State = alertStateView{st.State, st.Since, st.LastEval, st.LastValue, st.LastError}
	}
	return v
}

// apply validates in and copies it onto rule.
func (s *Server) applyRuleInput(in *alertRuleInput, rule *models.AlertRule) error {
	rule.Name = strings.TrimSpace(in.Name)
	if rule.Name == "" || len(rule.Name) > maxTitle {
		return errors.New("name is required (max 200 characters)")
	}
	rule.Query, rule.Comparator, rule.Threshold = strings.TrimSpace(in.Query), in.Comparator, in.Threshold
	rule.Window = orDefault(in.Window, "5m")
	rule.ForDuration = orDefault(in.ForDuration, "0s")
	rule.Cooldown = orDefault(in.Cooldown, "0s")
	rule.Severity = orDefault(in.Severity, "warning")
	if in.Enabled != nil {
		rule.Enabled = *in.Enabled
	}
	if err := ruler.Validate(rule); err != nil {
		return err
	}
	ids := in.NotifyChannelIDs
	if ids == nil {
		ids = []string{}
	}
	if len(ids) > maxChannelsPerRule {
		return errors.New("too many notification channels")
	}
	if len(ids) > 0 {
		var n int64
		if err := s.DB.Model(&models.NotificationChannel{}).Where("id IN ?", ids).Count(&n).Error; err != nil {
			return errors.New("could not verify channels")
		}
		if int(n) != len(dedupe(ids)) {
			return errors.New("unknown notification channel")
		}
	}
	b, _ := json.Marshal(ids)
	rule.NotifyChannelIDs = string(b)
	return nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	out := ids[:0:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) listAlertRules(w http.ResponseWriter, _ *http.Request) {
	var rules []models.AlertRule
	if err := s.DB.Order("name").Find(&rules).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	var states []models.AlertState
	if err := s.DB.Find(&states).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	byRule := make(map[string]*models.AlertState, len(states))
	for i := range states {
		byRule[states[i].RuleID] = &states[i]
	}
	out := make([]alertRuleView, 0, len(rules))
	for _, r := range rules {
		out = append(out, viewRule(r, byRule[r.ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ruleView(rule models.AlertRule) alertRuleView {
	var st models.AlertState
	res := s.DB.Where("rule_id = ?", rule.ID).Limit(1).Find(&st)
	if res.Error != nil || res.RowsAffected == 0 {
		return viewRule(rule, nil)
	}
	return viewRule(rule, &st)
}

func (s *Server) getAlertRule(w http.ResponseWriter, r *http.Request) {
	var rule models.AlertRule
	if !s.find(w, &rule, chi.URLParam(r, "id")) {
		return
	}
	writeJSON(w, http.StatusOK, s.ruleView(rule))
}

func (s *Server) createAlertRule(w http.ResponseWriter, r *http.Request) {
	var in alertRuleInput
	if !decode(w, r, &in) {
		return
	}
	rule := models.AlertRule{Enabled: true}
	if err := s.applyRuleInput(&in, &rule); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	p, _ := PrincipalFrom(r.Context())
	rule.OwnerID = p.UserID
	if err := s.DB.Create(&rule).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not save rule")
		return
	}
	writeJSON(w, http.StatusCreated, viewRule(rule, nil))
}

func (s *Server) updateAlertRule(w http.ResponseWriter, r *http.Request) {
	var in alertRuleInput
	if !decode(w, r, &in) {
		return
	}
	var rule models.AlertRule
	if !s.find(w, &rule, chi.URLParam(r, "id")) {
		return
	}
	if err := s.applyRuleInput(&in, &rule); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := s.DB.Save(&rule).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not save rule")
		return
	}
	writeJSON(w, http.StatusOK, s.ruleView(rule))
}

func (s *Server) deleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var rule models.AlertRule
	if !s.find(w, &rule, id) {
		return
	}
	// Delete children explicitly rather than relying on foreign-key enforcement.
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("rule_id = ?", id).Delete(&models.AlertEvent{}).Error; err != nil {
			return err
		}
		if err := tx.Where("rule_id = ?", id).Delete(&models.AlertState{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&models.AlertRule{}).Error
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- alert events ---

type alertEventView struct {
	ID          string    `json:"id"`
	RuleID      string    `json:"rule_id"`
	RuleName    string    `json:"rule_name"`
	State       string    `json:"state"`
	Value       float64   `json:"value"`
	At          time.Time `json:"at"`
	Notified    bool      `json:"notified"`
	NotifyError string    `json:"notify_error"`
}

// listAlertEvents serves GET /alert-events?rule_id=&limit= (newest first).
func (s *Server) listAlertEvents(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be between 1 and 500")
			return
		}
		limit = n
	}
	q := s.DB.Order("at DESC").Limit(limit)
	if id := r.URL.Query().Get("rule_id"); id != "" {
		q = q.Where("rule_id = ?", id)
	}
	var evs []models.AlertEvent
	if err := q.Find(&evs).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	var rules []models.AlertRule
	_ = s.DB.Select("id", "name").Find(&rules).Error
	names := make(map[string]string, len(rules))
	for _, ru := range rules {
		names[ru.ID] = ru.Name
	}
	out := make([]alertEventView, 0, len(evs))
	for _, e := range evs {
		out = append(out, alertEventView{e.ID, e.RuleID, names[e.RuleID], e.State, e.Value, e.At, e.Notified, e.NotifyError})
	}
	writeJSON(w, http.StatusOK, out)
}
