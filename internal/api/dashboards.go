package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cragone/Super-Argus/internal/models"
	"github.com/cragone/Super-Argus/internal/query"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

const (
	maxTitle      = 200
	maxJSONField  = 64 << 10
	maxPanelsPerD = 100
)

// --- saved queries ---

type savedQueryView struct {
	ID           string    `json:"id"`
	OwnerID      string    `json:"owner_id"`
	Name         string    `json:"name"`
	Query        string    `json:"query"`
	DefaultRange string    `json:"default_range"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func viewSaved(q models.SavedQuery) savedQueryView {
	return savedQueryView{q.ID, q.OwnerID, q.Name, q.Query, q.DefaultRange, q.CreatedAt, q.UpdatedAt}
}

type savedQueryInput struct {
	Name         string `json:"name"`
	Query        string `json:"query"`
	DefaultRange string `json:"default_range"`
}

// validateQueryText accepts a log query or a metric query.
func validateQueryText(q string) error {
	if strings.TrimSpace(q) == "" {
		return errors.New("query is required")
	}
	if query.IsMetric(q) {
		_, err := query.ParseMetric(q)
		return err
	}
	_, err := query.Parse(q)
	return err
}

func (in *savedQueryInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > maxTitle {
		return errors.New("name is required (max 200 characters)")
	}
	if err := validateQueryText(in.Query); err != nil {
		return err
	}
	if in.DefaultRange == "" {
		in.DefaultRange = "1h"
	}
	if len(in.DefaultRange) > 16 {
		return errors.New("default_range is too long")
	}
	return nil
}

func (s *Server) listSavedQueries(w http.ResponseWriter, _ *http.Request) {
	var qs []models.SavedQuery
	if err := s.DB.Order("name").Find(&qs).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	out := make([]savedQueryView, 0, len(qs))
	for _, q := range qs {
		out = append(out, viewSaved(q))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createSavedQuery(w http.ResponseWriter, r *http.Request) {
	var in savedQueryInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	p, _ := PrincipalFrom(r.Context())
	q := models.SavedQuery{OwnerID: p.UserID, Name: in.Name, Query: in.Query, DefaultRange: in.DefaultRange}
	if err := s.DB.Create(&q).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not save query")
		return
	}
	writeJSON(w, http.StatusCreated, viewSaved(q))
}

func (s *Server) updateSavedQuery(w http.ResponseWriter, r *http.Request) {
	var in savedQueryInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var q models.SavedQuery
	if !s.find(w, &q, chi.URLParam(r, "id")) {
		return
	}
	q.Name, q.Query, q.DefaultRange = in.Name, in.Query, in.DefaultRange
	if err := s.DB.Save(&q).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not save query")
		return
	}
	writeJSON(w, http.StatusOK, viewSaved(q))
}

func (s *Server) deleteSavedQuery(w http.ResponseWriter, r *http.Request) {
	s.deleteByID(w, &models.SavedQuery{}, chi.URLParam(r, "id"))
}

// --- dashboards and panels ---

type panelView struct {
	ID          string          `json:"id"`
	DashboardID string          `json:"dashboard_id"`
	Title       string          `json:"title"`
	Type        string          `json:"type"`
	Query       string          `json:"query"`
	Step        string          `json:"step"`
	Options     json.RawMessage `json:"options"`
	Position    int             `json:"position"`
}

type dashboardView struct {
	ID          string          `json:"id"`
	OwnerID     string          `json:"owner_id"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Layout      json.RawMessage `json:"layout"`
	Panels      []panelView     `json:"panels"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

func rawOr(s, def string) json.RawMessage {
	if s == "" {
		return json.RawMessage(def)
	}
	return json.RawMessage(s)
}

func viewPanel(p models.Panel) panelView {
	return panelView{p.ID, p.DashboardID, p.Title, p.Type, p.Query, p.Step, rawOr(p.Options, "{}"), p.Position}
}

func viewDashboard(d models.Dashboard, ps []models.Panel) dashboardView {
	v := dashboardView{ID: d.ID, OwnerID: d.OwnerID, Title: d.Title, Description: d.Description,
		Layout: rawOr(d.Layout, "{}"), Panels: make([]panelView, 0, len(ps)), CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
	for _, p := range ps {
		v.Panels = append(v.Panels, viewPanel(p))
	}
	return v
}

type dashboardInput struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Layout      json.RawMessage `json:"layout"`
}

func (in *dashboardInput) validate() (layout string, err error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > maxTitle {
		return "", errors.New("title is required (max 200 characters)")
	}
	if len(in.Description) > 2000 {
		return "", errors.New("description is too long (max 2000 characters)")
	}
	return normalizeJSON(in.Layout, "layout")
}

// normalizeJSON validates a JSON object/array field and returns its compact text.
func normalizeJSON(raw json.RawMessage, name string) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}", nil
	}
	if len(raw) > maxJSONField {
		return "", errors.New(name + " is too large")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", errors.New(name + " must be valid JSON")
	}
	switch v.(type) {
	case map[string]any, []any:
	default:
		return "", errors.New(name + " must be a JSON object or array")
	}
	b, _ := json.Marshal(v)
	return string(b), nil
}

func (s *Server) listDashboards(w http.ResponseWriter, _ *http.Request) {
	var ds []models.Dashboard
	if err := s.DB.Order("title").Find(&ds).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	out := make([]dashboardView, 0, len(ds))
	for _, d := range ds {
		out = append(out, viewDashboard(d, nil))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createDashboard(w http.ResponseWriter, r *http.Request) {
	var in dashboardInput
	if !decode(w, r, &in) {
		return
	}
	layout, err := in.validate()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	p, _ := PrincipalFrom(r.Context())
	d := models.Dashboard{OwnerID: p.UserID, Title: in.Title, Description: in.Description, Layout: layout}
	if err := s.DB.Create(&d).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not create dashboard")
		return
	}
	writeJSON(w, http.StatusCreated, viewDashboard(d, nil))
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	var d models.Dashboard
	if !s.find(w, &d, chi.URLParam(r, "id")) {
		return
	}
	var ps []models.Panel
	if err := s.DB.Where("dashboard_id = ?", d.ID).Order("position, created_at").Find(&ps).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, viewDashboard(d, ps))
}

func (s *Server) updateDashboard(w http.ResponseWriter, r *http.Request) {
	var in dashboardInput
	if !decode(w, r, &in) {
		return
	}
	layout, err := in.validate()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var d models.Dashboard
	if !s.find(w, &d, chi.URLParam(r, "id")) {
		return
	}
	d.Title, d.Description, d.Layout = in.Title, in.Description, layout
	if err := s.DB.Save(&d).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not update dashboard")
		return
	}
	var ps []models.Panel
	s.DB.Where("dashboard_id = ?", d.ID).Order("position, created_at").Find(&ps)
	writeJSON(w, http.StatusOK, viewDashboard(d, ps))
}

func (s *Server) deleteDashboard(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Panels go with it; done explicitly so it also works if SQLite foreign keys are off.
	_ = s.DB.Where("dashboard_id = ?", id).Delete(&models.Panel{}).Error
	s.deleteByID(w, &models.Dashboard{}, id)
}

type panelInput struct {
	Title    string          `json:"title"`
	Type     string          `json:"type"`
	Query    string          `json:"query"`
	Step     string          `json:"step"`
	Options  json.RawMessage `json:"options"`
	Position *int            `json:"position"`
}

func (in *panelInput) validate() (options string, err error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > maxTitle {
		return "", errors.New("title is required (max 200 characters)")
	}
	switch in.Type {
	case models.PanelLogs:
		if _, err := query.Parse(in.Query); err != nil {
			return "", err
		}
	case models.PanelTimeseries, models.PanelTopN, models.PanelStat:
		if _, err := query.ParseMetric(in.Query); err != nil {
			return "", err
		}
	default:
		return "", errors.New("type must be one of logs, timeseries, top_n, stat")
	}
	if in.Step != "" {
		d, err := query.ParseDuration(in.Step)
		if err != nil || d < time.Second {
			return "", errors.New("step must be a duration of at least 1s, e.g. 30s or 5m (or empty for automatic)")
		}
	}
	return normalizeJSON(in.Options, "options")
}

func (s *Server) createPanel(w http.ResponseWriter, r *http.Request) {
	var in panelInput
	if !decode(w, r, &in) {
		return
	}
	options, err := in.validate()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var d models.Dashboard
	if !s.find(w, &d, chi.URLParam(r, "id")) {
		return
	}
	var n int64
	s.DB.Model(&models.Panel{}).Where("dashboard_id = ?", d.ID).Count(&n)
	if n >= maxPanelsPerD {
		writeError(w, http.StatusUnprocessableEntity, "too_many_panels", "a dashboard can hold at most 100 panels")
		return
	}
	pos := int(n)
	if in.Position != nil {
		pos = *in.Position
	}
	p := models.Panel{DashboardID: d.ID, Title: in.Title, Type: in.Type, Query: strings.TrimSpace(in.Query), Step: in.Step, Options: options, Position: pos}
	if err := s.DB.Create(&p).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not create panel")
		return
	}
	writeJSON(w, http.StatusCreated, viewPanel(p))
}

func (s *Server) findPanel(w http.ResponseWriter, r *http.Request, p *models.Panel) bool {
	err := s.DB.Where("id = ? AND dashboard_id = ?", chi.URLParam(r, "panelId"), chi.URLParam(r, "id")).Limit(1).Find(p).Error
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "query failed")
		return false
	}
	if p.ID == "" {
		writeError(w, http.StatusNotFound, "not_found", "no such panel")
		return false
	}
	return true
}

func (s *Server) updatePanel(w http.ResponseWriter, r *http.Request) {
	var in panelInput
	if !decode(w, r, &in) {
		return
	}
	options, err := in.validate()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var p models.Panel
	if !s.findPanel(w, r, &p) {
		return
	}
	p.Title, p.Type, p.Query, p.Step, p.Options = in.Title, in.Type, strings.TrimSpace(in.Query), in.Step, options
	if in.Position != nil {
		p.Position = *in.Position
	}
	if err := s.DB.Save(&p).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not update panel")
		return
	}
	writeJSON(w, http.StatusOK, viewPanel(p))
}

func (s *Server) deletePanel(w http.ResponseWriter, r *http.Request) {
	var p models.Panel
	if !s.findPanel(w, r, &p) {
		return
	}
	if err := s.DB.Delete(&models.Panel{}, "id = ?", p.ID).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not delete panel")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// find loads a row by id into dst (which must be a pointer to a model with an ID column), or writes 404.
func (s *Server) find(w http.ResponseWriter, dst any, id string) bool {
	res := s.DB.Where("id = ?", id).Limit(1).Find(dst)
	if res.Error != nil {
		writeError(w, http.StatusInternalServerError, "internal", "query failed")
		return false
	}
	if res.RowsAffected == 0 {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return false
	}
	return true
}

func (s *Server) deleteByID(w http.ResponseWriter, model any, id string) {
	res := s.DB.Where("id = ?", id).Delete(model)
	if res.Error != nil && !errors.Is(res.Error, gorm.ErrRecordNotFound) {
		writeError(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	if res.RowsAffected == 0 {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
