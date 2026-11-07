package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/query"
	"github.com/klauspost/compress/snappy"
	"github.com/stretchr/testify/require"
)

// --- a tiny protobuf writer, the inverse of the decoder, to build Promtail-style bodies ---

func pbVarint(v uint64) []byte { return binary.AppendUvarint(nil, v) }

func pbField(num int, payload []byte) []byte {
	out := pbVarint(uint64(num<<3 | 2))
	out = append(out, pbVarint(uint64(len(payload)))...)
	return append(out, payload...)
}

func pbEntry(ts time.Time, line string) []byte {
	stamp := append(pbVarint(1<<3|0), pbVarint(uint64(ts.Unix()))...)
	stamp = append(stamp, pbVarint(2<<3|0)...)
	stamp = append(stamp, pbVarint(uint64(ts.Nanosecond()))...)
	return append(pbField(1, stamp), pbField(2, []byte(line))...)
}

func pbStream(labels string, entries ...[]byte) []byte {
	out := pbField(1, []byte(labels))
	for _, e := range entries {
		out = append(out, pbField(2, e)...)
	}
	return out
}

func pbPush(streams ...[]byte) []byte {
	var out []byte
	for _, s := range streams {
		out = append(out, pbField(1, s)...)
	}
	return out
}

func (r *rig) loki(body []byte, contentType string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/loki", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.h.Loki(rec, req)
	return rec
}

func (r *rig) lokiKeyHdr() map[string]string { return map[string]string{KeyHeader: r.apiKey} }

func tsNS(t time.Time) string { return fmt.Sprint(t.UnixNano()) }

func TestLokiJSONPush(t *testing.T) {
	r := newRig(t)
	body := fmt.Sprintf(`{"streams":[
	  {"stream":{"app.name":"web","env":"prod"},"values":[["%s","hello world"],["%s","second","ignored metadata"]]},
	  {"stream":{"app.name":"worker"},"values":[["%s","job done"]]}]}`, tsNS(t0), tsNS(t0.Add(time.Second)), tsNS(t0))
	rec := r.loki([]byte(body), "application/json", r.lokiKeyHdr())
	require.Equal(t, 204, rec.Code, rec.Body.String())

	require.NoError(t, r.eng.FlushAll())
	require.Equal(t, map[string]int{"hello world": 1, "second": 1, "job done": 1}, r.storedLines())

	svc := query.NewService(r.g, r.store, r.eng, query.DefaultLimits())
	res, err := svc.Range(context.Background(), "u", query.Request{Query: `{app_name="web", env="prod"}`,
		Start: t0.Add(-time.Minute).UnixNano(), End: t0.Add(time.Minute).UnixNano()})
	require.NoError(t, err)
	require.Len(t, res.Entries, 2, "label names are normalised to app_name")
}

func TestLokiProtobufSnappyPush(t *testing.T) {
	r := newRig(t)
	labels := `{service="api", msg="say \"hi\"", path="/a\\b"}`
	pb := pbPush(
		pbStream(labels, pbEntry(t0, "from promtail"), pbEntry(t0.Add(1500*time.Millisecond), "later")),
		pbStream(`{service="web"}`, pbEntry(t0, "other stream")),
	)
	rec := r.loki(snappy.Encode(nil, pb), "application/x-protobuf", r.lokiKeyHdr())
	require.Equal(t, 204, rec.Code, rec.Body.String())
	require.NoError(t, r.eng.FlushAll())
	require.Equal(t, map[string]int{"from promtail": 1, "later": 1, "other stream": 1}, r.storedLines())

	svc := query.NewService(r.g, r.store, r.eng, query.DefaultLimits())
	res, err := svc.Range(context.Background(), "u", query.Request{Query: `{service="api"}`,
		Start: t0.Add(-time.Minute).UnixNano(), End: t0.Add(time.Minute).UnixNano(), Backward: false})
	require.NoError(t, err)
	require.Len(t, res.Entries, 2)
	require.Equal(t, t0.UnixNano(), res.Entries[0].TS)
	require.Equal(t, t0.Add(1500*time.Millisecond).UnixNano(), res.Entries[1].TS, "seconds+nanos are combined")
	for _, l := range res.Streams {
		if l["service"] == "api" {
			require.Equal(t, `say "hi"`, l["msg"])
			require.Equal(t, `/a\b`, l["path"], "label escapes are decoded")
		}
	}
}

func TestLokiGzipJSON(t *testing.T) {
	r := newRig(t)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	fmt.Fprintf(zw, `{"streams":[{"stream":{"service":"api"},"values":[["%s","zipped"]]}]}`, tsNS(t0))
	require.NoError(t, zw.Close())
	hdr := r.lokiKeyHdr()
	hdr["Content-Encoding"] = "gzip"
	require.Equal(t, 204, r.loki(buf.Bytes(), "application/json", hdr).Code)
	require.NoError(t, r.eng.FlushAll())
	require.Equal(t, map[string]int{"zipped": 1}, r.storedLines())
}

func TestLokiAuthStyles(t *testing.T) {
	r := newRig(t)
	body := []byte(fmt.Sprintf(`{"streams":[{"stream":{"service":"a"},"values":[["%s","x"]]}]}`, tsNS(t0)))
	basic := func(user, pass string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}
	for name, hdr := range map[string]map[string]string{
		"x-argus-key":    {KeyHeader: r.apiKey},
		"bearer":         {"Authorization": "Bearer " + r.apiKey},
		"basic password": {"Authorization": basic("argus", r.apiKey)},
		"basic user":     {"Authorization": basic(r.apiKey, "")},
	} {
		require.Equal(t, 204, r.loki(body, "application/json", hdr).Code, name)
	}
	for name, hdr := range map[string]map[string]string{
		"none":       nil,
		"wrong key":  {"Authorization": "Bearer nope"},
		"wrong pass": {"Authorization": basic("argus", "nope")},
		"garbage":    {"Authorization": "Basic !!!"},
	} {
		require.Equal(t, 401, r.loki(body, "application/json", hdr).Code, name)
	}
}

func TestLokiRejectsBadInput(t *testing.T) {
	r := newRig(t)
	hdr := r.lokiKeyHdr()
	good := pbPush(pbStream(`{service="a"}`, pbEntry(t0, "x")))
	for name, tc := range map[string]struct {
		body []byte
		ct   string
	}{
		"invalid json":         {[]byte(`{"streams":`), "application/json"},
		"non-numeric ts":       {[]byte(`{"streams":[{"stream":{"a":"b"},"values":[["soon","x"]]}]}`), "application/json"},
		"numeric ts not str":   {[]byte(`{"streams":[{"stream":{"a":"b"},"values":[[123,"x"]]}]}`), "application/json"},
		"short value":          {[]byte(`{"streams":[{"stream":{"a":"b"},"values":[["1"]]}]}`), "application/json"},
		"bad snappy":           {[]byte("not snappy"), "application/x-protobuf"},
		"truncated protobuf":   {snappy.Encode(nil, good[:len(good)-3]), "application/x-protobuf"},
		"bad label set":        {snappy.Encode(nil, pbPush(pbStream(`service=a`, pbEntry(t0, "x")))), "application/x-protobuf"},
		"unterminated label":   {snappy.Encode(nil, pbPush(pbStream(`{a="b}`, pbEntry(t0, "x")))), "application/x-protobuf"},
		"too old for the node": {[]byte(fmt.Sprintf(`{"streams":[{"stream":{"a":"b"},"values":[["%s","x"]]}]}`, tsNS(t0.Add(-1000*time.Hour)))), "application/json"},
		"only empty lines":     {[]byte(fmt.Sprintf(`{"streams":[{"stream":{"a":"b"},"values":[["%s",""]]}]}`, tsNS(t0))), "application/json"},
	} {
		rec := r.loki(tc.body, tc.ct, hdr)
		require.Equal(t, 400, rec.Code, "%s: %s", name, rec.Body.String())
	}
	require.Equal(t, 204, r.loki([]byte(`{"streams":[]}`), "application/json", hdr).Code, "an empty push is fine")
}

func TestLokiPartialRejectStillAccepts(t *testing.T) {
	r := newRig(t)
	body := fmt.Sprintf(`{"streams":[{"stream":{"service":"a"},"values":[["%s","kept"],["%s","too old"]]}]}`, tsNS(t0), tsNS(t0.Add(-1000*time.Hour)))
	require.Equal(t, 204, r.loki([]byte(body), "application/json", r.lokiKeyHdr()).Code)
	require.NoError(t, r.eng.FlushAll())
	require.Equal(t, map[string]int{"kept": 1}, r.storedLines())
}

func TestLokiSharesRateLimitAndPolicy(t *testing.T) {
	r := newRig(t)
	p, err := ParsePolicy("email", "")
	require.NoError(t, err)
	r.h.Policy = p
	body := fmt.Sprintf(`{"streams":[{"stream":{"service":"a"},"values":[["%s","mail bob@example.com"]]}]}`, tsNS(t0))
	require.Equal(t, 204, r.loki([]byte(body), "application/json", r.lokiKeyHdr()).Code)
	require.NoError(t, r.eng.FlushAll())
	require.Equal(t, map[string]int{"mail [REDACTED:email]": 1}, r.storedLines(), "redaction applies to Loki pushes")

	r.h.Limits.DefaultLinesPerSec, r.h.Limits.BurstFactor = 1, 1
	var many strings.Builder
	many.WriteString(`{"streams":[{"stream":{"service":"a"},"values":[`)
	for i := 0; i < 50; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `["%s","l%d"]`, tsNS(t0), i)
	}
	many.WriteString(`]}]}`)
	rec := r.loki([]byte(many.String()), "application/json", r.lokiKeyHdr())
	require.Contains(t, []int{413, 429}, rec.Code, "per-key limits apply: %s", rec.Body.String())
}

func TestParseLabelSetAndNames(t *testing.T) {
	got, err := parseLabelSet(`{a="1",b = "x y", c="q\"z"}`)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"a": "1", "b": "x y", "c": `q"z`}, got)
	got, err = parseLabelSet(`{}`)
	require.NoError(t, err)
	require.Empty(t, got)

	require.Equal(t, "app_name", lokiLabelName("app.name"))
	require.Equal(t, "k8s_pod_name", lokiLabelName("k8s-pod.name"))
	require.Equal(t, "_9lives", lokiLabelName("9lives"))
	require.Equal(t, "service", lokiLabelName("service"))
}
