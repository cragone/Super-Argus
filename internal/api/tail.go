package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/cragone/Super-Argus/internal/query"
	"github.com/cragone/Super-Argus/internal/tail"
	"github.com/cragone/Super-Argus/internal/wal"
)

// Variables so tests can shorten them.
var (
	tailHeartbeat    = 15 * time.Second
	tailWriteTimeout = 10 * time.Second
)

type tailEntry struct {
	TS     string            `json:"ts"`
	Stream string            `json:"stream"`
	Labels map[string]string `json:"labels"`
	Line   string            `json:"line"`
	Fields map[string]string `json:"fields,omitempty"`
}

type tailStreamInfo struct {
	labels map[string]string
	match  bool
}

// tailStream serves GET /tail?query= as server-sent events.
//
// Events: ready (stream is live), entry (one matching line), dropped
// ({"count":N}: the client was too slow and N lines were skipped), expired (the
// access token ran out; reconnect with a fresh one). Comment lines are heartbeats.
func (s *Server) tailStream(w http.ResponseWriter, r *http.Request) {
	q, err := query.Parse(r.URL.Query().Get("query"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_query", err.Error())
		return
	}
	pipe, err := query.Compile(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_query", err.Error())
		return
	}
	p, _ := PrincipalFrom(r.Context())
	sub, err := s.Tail.Subscribe(p.UserID)
	if errors.Is(err, tail.ErrTooManyTails) {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "too_many_tails", "too many live tail connections; close another tail and retry")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not start tail")
		return
	}
	defer sub.Close()

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // do not let nginx buffer the stream
	w.WriteHeader(http.StatusOK)

	write := func(b []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(tailWriteTimeout)) // a stalled client is disconnected, not waited on
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write([]byte("event: ready\ndata: {}\n\n")) {
		return
	}

	var expired <-chan time.Time
	if !p.Expires.IsZero() {
		t := time.NewTimer(time.Until(p.Expires))
		defer t.Stop()
		expired = t.C
	}
	beat := time.NewTicker(tailHeartbeat)
	defer beat.Stop()

	streams := map[uint64]*tailStreamInfo{}
	var buf bytes.Buffer
	reportDropped := func() bool {
		if n := sub.TakeDropped(); n > 0 {
			return write([]byte("event: dropped\ndata: {\"count\":" + strconv.FormatInt(n, 10) + "}\n\n"))
		}
		return true
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-expired:
			write([]byte("event: expired\ndata: {}\n\n"))
			return
		case <-beat.C:
			if !write([]byte(": ping\n\n")) || !reportDropped() {
				return
			}
		case recs := <-sub.C():
			buf.Reset()
			s.encodeTail(&buf, pipe, streams, recs)
			if buf.Len() > 0 && !write(buf.Bytes()) {
				return
			}
			if !reportDropped() {
				return
			}
		}
	}
}

// encodeTail appends an SSE entry event for every record that matches the query.
func (s *Server) encodeTail(buf *bytes.Buffer, pipe *query.Pipeline, streams map[uint64]*tailStreamInfo, recs []wal.Record) {
	for _, rec := range recs {
		info, ok := streams[rec.StreamID]
		if !ok {
			labels, err := s.Index.Labels(rec.StreamID)
			if err != nil {
				continue // stream vanished (retention); skip rather than fail the tail
			}
			info = &tailStreamInfo{labels: labels, match: pipe.MatchLabels(labels)}
			streams[rec.StreamID] = info
		}
		if !info.match {
			continue
		}
		fields, keep := pipe.Process(info.labels, rec.Line)
		if !keep {
			continue
		}
		b, err := json.Marshal(tailEntry{
			TS: strconv.FormatInt(rec.TS, 10), Stream: query.FormatStreamID(rec.StreamID),
			Labels: info.labels, Line: rec.Line, Fields: fields,
		})
		if err != nil {
			continue
		}
		buf.WriteString("event: entry\ndata: ")
		buf.Write(b)
		buf.WriteString("\n\n")
	}
}
