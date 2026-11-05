package ingest

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cragone/Super-Argus/internal/auth"
	"github.com/cragone/Super-Argus/internal/index"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/cragone/Super-Argus/internal/wal"
	"gorm.io/gorm"
)

const (
	// KeyHeader carries the ingest API key.
	KeyHeader = "X-Argus-Key"
	// LabelsHeader carries default labels for the whole batch: k=v,k2=v2.
	LabelsHeader = "X-Argus-Labels"

	truncMarker   = "...[truncated]"
	maxErrorsEcho = 10
	touchEvery    = time.Minute
)

var labelNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Handler serves POST /ingest/push.
type Handler struct {
	DB     *gorm.DB
	Engine *Engine
	Index  *index.Index
	Limits Limits
	Now    func() time.Time

	lim     *limiter
	touchMu sync.Mutex
	touched map[string]time.Time
}

// NewHandler builds a push handler.
func NewHandler(db *gorm.DB, e *Engine, ix *index.Index, l Limits) *Handler {
	return &Handler{DB: db, Engine: e, Index: ix, Limits: l, Now: time.Now, lim: newLimiter(), touched: map[string]time.Time{}}
}

type pushLine struct {
	TS     json.RawMessage   `json:"ts"`
	Labels map[string]string `json:"labels"`
	Line   *string           `json:"line"`
}

type pending struct {
	labels map[string]string
	text   string
	ts     int64
	line   string
	n      int // 1-based input line number
}

type pushResponse struct {
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Errors   []string `json:"errors,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func (h *Handler) authenticate(r *http.Request) (*models.APIKey, bool) {
	k := r.Header.Get(KeyHeader)
	if k == "" {
		return nil, false
	}
	var keys []models.APIKey
	if err := h.DB.Where("key_hash = ? AND revoked_at IS NULL", auth.HashAPIKey(k)).Limit(1).Find(&keys).Error; err != nil || len(keys) == 0 {
		return nil, false
	}
	key := keys[0]
	now := h.Now()
	h.touchMu.Lock()
	due := now.Sub(h.touched[key.ID]) >= touchEvery
	if due {
		h.touched[key.ID] = now
	}
	h.touchMu.Unlock()
	if due {
		h.DB.Model(&models.APIKey{}).Where("id = ?", key.ID).Update("last_used_at", now)
	}
	return &key, true
}

// Push ingests an NDJSON batch: {"ts":..., "labels":{...}, "line":"..."} per line.
func (h *Handler) Push(w http.ResponseWriter, r *http.Request) {
	key, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "missing, invalid or revoked API key")
		return
	}
	defaults, err := parseLabelHeader(r.Header.Get(LabelsHeader))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	body, err := h.bodyReader(w, r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid gzip body")
		return
	}

	now := h.Now()
	var resp pushResponse
	var batch []pending
	var rawBytes int
	reject := func(n int, msg string) {
		resp.Rejected++
		if len(resp.Errors) < maxErrorsEcho {
			resp.Errors = append(resp.Errors, fmt.Sprintf("line %d: %s", n, msg))
		}
	}

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), int(h.Limits.MaxBodyBytes))
	for n := 1; sc.Scan(); n++ {
		raw := sc.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		p, err := h.parseLine(raw, defaults, now)
		if err != nil {
			reject(n, err.Error())
			continue
		}
		p.n = n
		rawBytes += len(p.line)
		batch = append(batch, p)
	}
	if err := sc.Err(); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) || errors.Is(err, bufio.ErrTooLong) {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large",
				fmt.Sprintf("request body exceeds %d bytes", h.Limits.MaxBodyBytes))
			return
		}
		writeErr(w, http.StatusBadRequest, "bad_request", "could not read body")
		return
	}

	if len(batch) > 0 {
		lps := key.RateLimit
		if lps <= 0 {
			lps = h.Limits.DefaultLinesPerSec
		}
		switch h.lim.take(key.ID, len(batch), rawBytes, lps, h.Limits.BytesPerSec, h.Limits.BurstFactor, now) {
		case tooLarge:
			writeErr(w, http.StatusRequestEntityTooLarge, "batch_too_large", "batch exceeds this key's burst capacity; send smaller batches")
			return
		case throttled:
			w.Header().Set("Retry-After", "1")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded for this API key")
			return
		}
	}

	recs := make([]wal.Record, 0, len(batch))
	ids := map[string]uint64{}
	bad := map[string]string{}
	for _, p := range batch {
		if msg, skip := bad[p.text]; skip {
			reject(p.n, msg)
			continue
		}
		id, seen := ids[p.text]
		if !seen {
			id, err = h.Index.Resolve(key.ID, p.labels, h.Limits.MaxStreamsPerKey)
			if errors.Is(err, index.ErrStreamLimit) {
				bad[p.text] = err.Error()
				reject(p.n, err.Error())
				continue
			}
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "internal", "could not resolve stream")
				return
			}
			ids[p.text] = id
		}
		recs = append(recs, wal.Record{StreamID: id, TS: p.ts, Line: p.line})
	}
	if len(recs) > 0 {
		if err := h.Engine.Append(recs); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "could not persist lines")
			return
		}
	}
	resp.Accepted = len(recs)
	status := http.StatusOK
	if resp.Accepted == 0 && resp.Rejected > 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, resp)
}

func (h *Handler) bodyReader(w http.ResponseWriter, r *http.Request) (io.Reader, error) {
	var rc io.ReadCloser = http.MaxBytesReader(w, r.Body, h.Limits.MaxBodyBytes)
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(rc)
		if err != nil {
			return nil, err
		}
		// Bound the decompressed size too, so a small gzip bomb cannot expand freely.
		rc = http.MaxBytesReader(w, gz, h.Limits.MaxBodyBytes)
	}
	return rc, nil
}

func (h *Handler) parseLine(raw []byte, defaults map[string]string, now time.Time) (pending, error) {
	var in pushLine
	if err := json.Unmarshal(raw, &in); err != nil {
		return pending{}, errors.New("invalid JSON")
	}
	if in.Line == nil || *in.Line == "" {
		return pending{}, errors.New(`missing "line"`)
	}
	ts, err := parseTS(in.TS, now)
	if err != nil {
		return pending{}, err
	}
	if age := now.Sub(time.Unix(0, ts)); age > h.Limits.MaxAge {
		return pending{}, fmt.Errorf("timestamp older than %s", h.Limits.MaxAge)
	} else if -age > h.Limits.MaxFuture {
		return pending{}, fmt.Errorf("timestamp more than %s in the future", h.Limits.MaxFuture)
	}
	labels := make(map[string]string, len(defaults)+len(in.Labels))
	for k, v := range defaults {
		labels[k] = v
	}
	for k, v := range in.Labels {
		labels[k] = v
	}
	if err := h.validateLabels(labels); err != nil {
		return pending{}, err
	}
	return pending{labels: labels, text: index.LabelsText(labels), ts: ts, line: h.truncate(*in.Line)}, nil
}

func (h *Handler) validateLabels(labels map[string]string) error {
	if len(labels) == 0 {
		return errors.New("at least one label is required")
	}
	if len(labels) > h.Limits.MaxLabels {
		return fmt.Errorf("too many labels (%d, max %d)", len(labels), h.Limits.MaxLabels)
	}
	for k, v := range labels {
		if !labelNameRE.MatchString(k) {
			return fmt.Errorf("invalid label name %q", k)
		}
		if len(v) > h.Limits.MaxLabelValueBytes {
			return fmt.Errorf("value of label %q exceeds %d bytes", k, h.Limits.MaxLabelValueBytes)
		}
		if !utf8.ValidString(v) {
			return fmt.Errorf("value of label %q is not valid UTF-8", k)
		}
	}
	return nil
}

func (h *Handler) truncate(s string) string {
	if len(s) <= h.Limits.MaxLineBytes {
		return s
	}
	cut := h.Limits.MaxLineBytes - len(truncMarker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncMarker
}

func parseTS(raw json.RawMessage, now time.Time) (int64, error) {
	s := strings.TrimSpace(string(raw))
	switch {
	case s == "" || s == "null":
		return now.UnixNano(), nil
	case s[0] == '"':
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return 0, errors.New("invalid ts")
		}
		if t, err := time.Parse(time.RFC3339Nano, str); err == nil {
			return t.UnixNano(), nil
		}
		s = str
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("ts must be RFC3339 or a positive integer of epoch nanoseconds")
	}
	return n, nil
}

func parseLabelHeader(v string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(v) == "" {
		return out, nil
	}
	for _, kv := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid %s header: expected k=v,k2=v2", LabelsHeader)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(val)
	}
	return out, nil
}
