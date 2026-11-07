package ingest

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/snappy"
)

// Loki serves the Loki push API so existing shippers (Promtail, Grafana Alloy,
// Fluent Bit, Vector, Logstash) can point at Argus unchanged:
//
//	POST /api/v1/ingest/loki    (also served at /loki/api/v1/push)
//
// Both Loki wire formats are accepted: JSON ({"streams":[{"stream":{...},
// "values":[["<unix ns>","line"]]}]}) and the default protobuf compressed with
// snappy. Authenticate with the Argus API key as X-Argus-Key, a bearer token,
// or the password (or username) of HTTP basic auth. Structured metadata is
// ignored. Label names Loki would normalise (dots, dashes) are rewritten with
// underscores. Success is 204 No Content, as Loki answers.
func (h *Handler) Loki(w http.ResponseWriter, r *http.Request) {
	key, ok := h.authenticateKey(lokiKey(r))
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
	raw, err := io.ReadAll(body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", fmt.Sprintf("request body exceeds %d bytes", h.Limits.MaxBodyBytes))
			return
		}
		writeErr(w, http.StatusBadRequest, "bad_request", "could not read body")
		return
	}

	var streams []lokiStream
	if ct := strings.ToLower(r.Header.Get("Content-Type")); strings.Contains(ct, "protobuf") || strings.Contains(ct, "snappy") {
		if n, err := snappy.DecodedLen(raw); err != nil || int64(n) > h.Limits.MaxBodyBytes {
			writeErr(w, http.StatusBadRequest, "bad_request", "invalid or oversized snappy body")
			return
		}
		pb, err := snappy.Decode(nil, raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "invalid snappy body")
			return
		}
		if streams, err = decodePushProto(pb); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "invalid protobuf body: "+err.Error())
			return
		}
	} else if streams, err = decodePushJSON(raw); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}

	now := h.Now()
	var resp pushResponse
	var batch []pending
	var rawBytes, n int
	for _, st := range streams {
		base := make(map[string]string, len(defaults)+len(st.labels))
		for k, v := range defaults {
			base[k] = v
		}
		for k, v := range st.labels {
			base[lokiLabelName(k)] = v
		}
		for _, e := range st.entries {
			n++
			if e.line == "" {
				resp.reject(n, `empty line`)
				continue
			}
			labels := make(map[string]string, len(base)) // makePending keeps the map, so each line gets its own
			for k, v := range base {
				labels[k] = v
			}
			p, err := h.makePending(labels, e.ts, e.line, now)
			if err != nil {
				resp.reject(n, err.Error())
				continue
			}
			p.n = n
			if !h.Policy.Keep(p.labels, p.line) {
				resp.Sampled++
				continue
			}
			p.line = h.Policy.Redact(p.line)
			rawBytes += len(p.line)
			batch = append(batch, p)
		}
	}
	if f := h.commit(key, batch, rawBytes, now, &resp); f != nil {
		f.write(w)
		return
	}
	if resp.Accepted == 0 && resp.Rejected > 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", strings.Join(resp.Errors, "; "))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// lokiKey finds the API key in a Loki request: X-Argus-Key, a bearer token, or basic auth.
func lokiKey(r *http.Request) string {
	if k := r.Header.Get(KeyHeader); k != "" {
		return k
	}
	a := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(a, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	if t, ok := strings.CutPrefix(a, "Basic "); ok {
		if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(t)); err == nil {
			user, pass, _ := strings.Cut(string(dec), ":")
			if pass != "" {
				return pass
			}
			return user
		}
	}
	return ""
}

// lokiLabelName rewrites characters Argus label names do not allow.
func lokiLabelName(s string) string {
	if labelNameRE.MatchString(s) {
		return s
	}
	if s != "" && s[0] >= '0' && s[0] <= '9' {
		s = "_" + s // a label may not start with a digit
	}
	b := []byte(s)
	for i, c := range b {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '_'
		}
	}
	return string(b)
}

type lokiEntry struct {
	ts   int64
	line string
}

type lokiStream struct {
	labels  map[string]string
	entries []lokiEntry
}

// maxLokiStreams bounds the work one request can ask for beyond the body-size limit.
const maxLokiStreams = 10000

func decodePushJSON(raw []byte) ([]lokiStream, error) {
	var in struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	if len(in.Streams) > maxLokiStreams {
		return nil, errors.New("too many streams")
	}
	out := make([]lokiStream, 0, len(in.Streams))
	for _, s := range in.Streams {
		ls := lokiStream{labels: s.Stream}
		for _, v := range s.Values {
			if len(v) < 2 {
				return nil, errors.New(`each value must be ["<unix ns>", "<line>"]`)
			}
			var tsStr, line string
			if err := json.Unmarshal(v[0], &tsStr); err != nil {
				return nil, errors.New("timestamp must be a string of unix nanoseconds")
			}
			ts, err := strconv.ParseInt(tsStr, 10, 64)
			if err != nil || ts <= 0 {
				return nil, errors.New("timestamp must be a string of unix nanoseconds")
			}
			if err := json.Unmarshal(v[1], &line); err != nil {
				return nil, errors.New("line must be a string")
			}
			ls.entries = append(ls.entries, lokiEntry{ts, line})
		}
		out = append(out, ls)
	}
	return out, nil
}

// --- protobuf (logproto.PushRequest) ---
//
// PushRequest  { repeated Stream streams = 1; }
// Stream       { string labels = 1; repeated Entry entries = 2; }
// Entry        { Timestamp timestamp = 1; string line = 2; ... }
// Timestamp    { int64 seconds = 1; int32 nanos = 2; }
//
// Only these fields are read; everything else (hash, structured metadata) is skipped.

type pbReader struct{ b []byte }

func (r *pbReader) varint() (uint64, error) {
	var v uint64
	for i := 0; i < 10; i++ {
		if i >= len(r.b) {
			return 0, errors.New("truncated varint")
		}
		c := r.b[i]
		v |= uint64(c&0x7f) << (7 * uint(i))
		if c < 0x80 {
			r.b = r.b[i+1:]
			return v, nil
		}
	}
	return 0, errors.New("varint too long")
}

// next returns the next field number and wire type, with its payload for
// length-delimited fields, or (0, 0, nil, io.EOF) at the end.
func (r *pbReader) next() (num int, wire int, payload []byte, varint uint64, err error) {
	if len(r.b) == 0 {
		return 0, 0, nil, 0, io.EOF
	}
	tag, err := r.varint()
	if err != nil {
		return 0, 0, nil, 0, err
	}
	num, wire = int(tag>>3), int(tag&7)
	switch wire {
	case 0:
		varint, err = r.varint()
	case 1:
		if len(r.b) < 8 {
			return 0, 0, nil, 0, errors.New("truncated fixed64")
		}
		r.b = r.b[8:]
	case 5:
		if len(r.b) < 4 {
			return 0, 0, nil, 0, errors.New("truncated fixed32")
		}
		r.b = r.b[4:]
	case 2:
		var l uint64
		if l, err = r.varint(); err != nil {
			return 0, 0, nil, 0, err
		}
		if l > uint64(len(r.b)) {
			return 0, 0, nil, 0, errors.New("truncated field")
		}
		payload, r.b = r.b[:l], r.b[l:]
	default:
		return 0, 0, nil, 0, fmt.Errorf("unsupported wire type %d", wire)
	}
	return num, wire, payload, varint, err
}

func decodePushProto(b []byte) ([]lokiStream, error) {
	var out []lokiStream
	r := &pbReader{b}
	for {
		num, wire, payload, _, err := r.next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if num != 1 || wire != 2 {
			continue
		}
		if len(out) >= maxLokiStreams {
			return nil, errors.New("too many streams")
		}
		st, err := decodeProtoStream(payload)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
}

func decodeProtoStream(b []byte) (lokiStream, error) {
	var st lokiStream
	r := &pbReader{b}
	for {
		num, wire, payload, _, err := r.next()
		if err == io.EOF {
			return st, nil
		}
		if err != nil {
			return st, err
		}
		switch {
		case num == 1 && wire == 2:
			if st.labels, err = parseLabelSet(string(payload)); err != nil {
				return st, err
			}
		case num == 2 && wire == 2:
			e, err := decodeProtoEntry(payload)
			if err != nil {
				return st, err
			}
			st.entries = append(st.entries, e)
		}
	}
}

func decodeProtoEntry(b []byte) (lokiEntry, error) {
	var e lokiEntry
	r := &pbReader{b}
	for {
		num, wire, payload, _, err := r.next()
		if err == io.EOF {
			return e, nil
		}
		if err != nil {
			return e, err
		}
		switch {
		case num == 1 && wire == 2:
			var secs, nanos int64
			tr := &pbReader{payload}
			for {
				n2, w2, _, v, err := tr.next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return e, err
				}
				if w2 == 0 && n2 == 1 {
					secs = int64(v)
				} else if w2 == 0 && n2 == 2 {
					nanos = int64(int32(v))
				}
			}
			e.ts = secs*int64(time.Second) + nanos
		case num == 2 && wire == 2:
			e.line = string(payload)
		}
	}
}

// parseLabelSet parses Loki's label string: {a="b", c="d\"e"}.
func parseLabelSet(s string) (map[string]string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, fmt.Errorf("bad label set %q", s)
	}
	s = s[1 : len(s)-1]
	out := map[string]string{}
	for {
		s = strings.TrimLeft(s, " ,")
		if s == "" {
			return out, nil
		}
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			return nil, errors.New("bad label set")
		}
		name := strings.TrimSpace(s[:eq])
		rest := strings.TrimLeft(s[eq+1:], " ")
		if name == "" || rest == "" || rest[0] != '"' {
			return nil, errors.New("bad label set")
		}
		rest = rest[1:]
		var val strings.Builder
		i := 0
		for ; i < len(rest) && rest[i] != '"'; i++ {
			if rest[i] == '\\' && i+1 < len(rest) {
				i++
				switch rest[i] {
				case 'n':
					val.WriteByte('\n')
				case 't':
					val.WriteByte('\t')
				default:
					val.WriteByte(rest[i])
				}
				continue
			}
			val.WriteByte(rest[i])
		}
		if i >= len(rest) {
			return nil, errors.New("unterminated label value")
		}
		out[name] = val.String()
		s = rest[i+1:]
	}
}
