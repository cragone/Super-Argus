package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cragone/Super-Argus/internal/query"
	"github.com/go-chi/chi/v5"
)

// parseTime accepts RFC3339 or integer unix nanoseconds.
func parseTime(v string) (int64, error) {
	if v == "" {
		return 0, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UnixNano(), nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("must be RFC3339 or unix nanoseconds")
	}
	return n, nil
}

func writeQueryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, query.ErrBadRequest):
		writeError(w, http.StatusBadRequest, "bad_query", err.Error())
	case errors.Is(err, query.ErrLimit):
		writeError(w, http.StatusUnprocessableEntity, "query_limit", err.Error())
	case errors.Is(err, query.ErrBusy):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "too_many_queries", err.Error())
	case errors.Is(err, query.ErrTimeout):
		writeError(w, http.StatusGatewayTimeout, "query_timeout", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", "query failed")
	}
}

type queryEntry struct {
	TS     string            `json:"ts"` // unix nanoseconds as a string (exceeds JS number precision)
	Stream string            `json:"stream"`
	Line   string            `json:"line"`
	Fields map[string]string `json:"fields,omitempty"`
}

type rangeResponse struct {
	Entries []queryEntry                 `json:"entries"`
	Streams map[string]map[string]string `json:"streams"`
	Stats   query.Stats                  `json:"stats"`
}

// queryRange serves GET /query/range?query=&start=&end=&limit=&direction=backward|forward.
func (s *Server) queryRange(w http.ResponseWriter, r *http.Request) {
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
	limit := 0
	if v := qv.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return
		}
	}
	dir := strings.ToLower(qv.Get("direction"))
	if dir != "" && dir != "backward" && dir != "forward" {
		writeError(w, http.StatusBadRequest, "bad_request", "direction must be backward or forward")
		return
	}
	p, _ := PrincipalFrom(r.Context())
	began := time.Now()
	res, err := s.Query.Range(r.Context(), p.UserID, query.Request{
		Query: qv.Get("query"), Start: start, End: end, Limit: limit, Backward: dir != "forward",
	})
	s.Metrics.Query("log", time.Since(began), err)
	if err != nil {
		writeQueryError(w, err)
		return
	}
	out := rangeResponse{Entries: make([]queryEntry, 0, len(res.Entries)), Streams: map[string]map[string]string{}, Stats: res.Stats}
	for _, e := range res.Entries {
		out.Entries = append(out.Entries, queryEntry{TS: strconv.FormatInt(e.TS, 10), Stream: query.FormatStreamID(e.Stream), Line: e.Line, Fields: e.Fields})
	}
	for id, labels := range res.Streams {
		out.Streams[query.FormatStreamID(id)] = labels
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) timeRange(w http.ResponseWriter, r *http.Request) (start, end int64, ok bool) {
	var err error
	if start, err = parseTime(r.URL.Query().Get("start")); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "start: "+err.Error())
		return 0, 0, false
	}
	if end, err = parseTime(r.URL.Query().Get("end")); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "end: "+err.Error())
		return 0, 0, false
	}
	return start, end, true
}

func (s *Server) labelNames(w http.ResponseWriter, r *http.Request) {
	start, end, ok := s.timeRange(w, r)
	if !ok {
		return
	}
	names, err := s.Query.Labels(r.Context(), start, end)
	if err != nil {
		writeQueryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(names)})
}

func (s *Server) labelValues(w http.ResponseWriter, r *http.Request) {
	start, end, ok := s.timeRange(w, r)
	if !ok {
		return
	}
	vals, err := s.Query.LabelValues(r.Context(), chi.URLParam(r, "name"), start, end)
	if err != nil {
		writeQueryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(vals)})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
