package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/cragone/Super-Argus/internal/query"
)

type metricSeries struct {
	Labels map[string]string `json:"labels"`
	Values [][2]float64      `json:"values"` // [unix ms, value]
}

type metricResponse struct {
	Series  []metricSeries `json:"series"`
	StartMS int64          `json:"start_ms"`
	EndMS   int64          `json:"end_ms"`
	StepMS  int64          `json:"step_ms"`
	Stats   query.Stats    `json:"stats"`
}

// parseStep accepts a duration (30s, 5m) or a number of seconds.
func parseStep(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	if d, err := query.ParseDuration(v); err == nil {
		return d, nil
	}
	secs, err := strconv.ParseFloat(v, 64)
	if err != nil || secs <= 0 {
		return 0, errors.New("must be a duration like 30s or a number of seconds")
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// queryMetric serves GET /query/metric?query=&start=&end=&step=.
func (s *Server) queryMetric(w http.ResponseWriter, r *http.Request) {
	qv := r.URL.Query()
	start, err := parseTime(qv.Get("start"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "start: "+err.Error())
		return
	}
	end, err := parseTime(qv.Get("end"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "end: "+err.Error())
		return
	}
	step, err := parseStep(qv.Get("step"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "step: "+err.Error())
		return
	}
	p, _ := PrincipalFrom(r.Context())
	res, err := s.Query.Metric(r.Context(), p.UserID, query.MetricRequest{Query: qv.Get("query"), Start: start, End: end, Step: step})
	if err != nil {
		writeQueryError(w, err)
		return
	}
	out := metricResponse{
		Series:  make([]metricSeries, 0, len(res.Series)),
		StartMS: res.Start / 1e6, EndMS: res.End / 1e6, StepMS: res.Step.Milliseconds(), Stats: res.Stats,
	}
	for _, sr := range res.Series {
		ms := metricSeries{Labels: sr.Labels, Values: make([][2]float64, 0, len(sr.Points))}
		for _, pt := range sr.Points {
			ms.Values = append(ms.Values, [2]float64{float64(pt.T), pt.V})
		}
		out.Series = append(out.Series, ms)
	}
	writeJSON(w, http.StatusOK, out)
}
