package api

import (
	"net/http"
	"time"
)

// defaultServiceLabel is the stream label whose values are listed as services.
const defaultServiceLabel = "service"

// listServices serves GET /services: every value of the service label with
// activity and volume. Optional start/end bound the window; ?label= overrides
// the label key.
func (s *Server) listServices(w http.ResponseWriter, r *http.Request) {
	start, end, ok := s.timeRange(w, r)
	if !ok {
		return
	}
	key := r.URL.Query().Get("label")
	if key == "" {
		key = defaultServiceLabel
	}
	svcs, err := s.Query.Services(r.Context(), key, start, end, time.Now())
	if err != nil {
		writeQueryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"label": key, "data": svcs})
}
