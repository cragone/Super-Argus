package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cragone/Super-Argus/internal/metrics"
	"github.com/stretchr/testify/require"
)

func TestParsePolicy(t *testing.T) {
	p, err := ParsePolicy("", "")
	require.NoError(t, err)
	require.Nil(t, p, "no settings means no policy")

	for _, bad := range [][2]string{{"ssn", ""}, {"", "api"}, {"", "api=abc"}, {"", "api=101"}, {"", "=5"}} {
		_, err := ParsePolicy(bad[0], bad[1])
		require.Error(t, err, "%v", bad)
	}
	p, err = ParsePolicy("all", "api=25, *=50")
	require.NoError(t, err)
	require.Len(t, p.redact, 4)
	require.Equal(t, 25.0, p.sample["api"])
}

func TestRedact(t *testing.T) {
	p, err := ParsePolicy("all", "")
	require.NoError(t, err)
	var nilPolicy *Policy
	require.Equal(t, "x", nilPolicy.Redact("x"))

	for in, want := range map[string]string{
		"user bob@example.com logged in":     "user [REDACTED:email] logged in",
		"Authorization: Bearer abc.DEF-123=": "Authorization: [REDACTED:bearer]",
		"card 4111 1111 1111 1111 declined":  "card [REDACTED:card] declined",
		"card 4111-1111-1111-1111":           "card [REDACTED:card]",
		"from 10.1.2.3 port 22":              "from [REDACTED:ip] port 22",
		"status 200 in 35ms, order 12345":    "status 200 in 35ms, order 12345",
	} {
		require.Equal(t, want, p.Redact(in), in)
	}

	only, _ := ParsePolicy("email", "")
	require.Equal(t, "ip 10.1.2.3 [REDACTED:email]", only.Redact("ip 10.1.2.3 a@b.io"))
}

func TestSampling(t *testing.T) {
	p, err := ParsePolicy("", "api=25,*=50,keepall=100")
	require.NoError(t, err)
	r := 0.0
	p.rnd = func() float64 { return r }

	info := map[string]string{"service": "api"}
	r = 0.10
	require.True(t, p.Keep(info, "hello"), "10 < 25 percent")
	r = 0.30
	require.False(t, p.Keep(info, "hello"), "30 >= 25 percent")

	require.True(t, p.Keep(map[string]string{"service": "api", "level": "error"}, "boom"), "label level wins")
	require.True(t, p.Keep(info, `{"level":"warn","msg":"x"}`), "JSON level")
	require.True(t, p.Keep(info, `ts=1 level=ERROR msg=x`), "logfmt level, case-insensitive")
	require.False(t, p.Keep(info, `{"level":"info"}`))
	require.False(t, p.Keep(info, `{"level":"debug"}`))

	r = 0.60
	require.False(t, p.Keep(map[string]string{"service": "other"}, "x"), "* fallback at 50 percent")
	require.True(t, p.Keep(map[string]string{"service": "keepall"}, "x"))

	noFallback, _ := ParsePolicy("", "api=0")
	require.True(t, noFallback.Keep(map[string]string{"service": "other"}, "x"), "services without a rule keep everything")
	var nilPolicy *Policy
	require.True(t, nilPolicy.Keep(info, "x"))
}

func TestPushAppliesPolicy(t *testing.T) {
	r := newRig(t)
	p, err := ParsePolicy("email", "api=0")
	require.NoError(t, err)
	r.h.Policy = p

	body := line(t0, "api", "debug noise") +
		line(t0, "api", `{"level":"error","msg":"mail bob@example.com failed"}`) +
		line(t0, "worker", "job for amy@example.com done")
	rec := r.push(body, nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var resp pushResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 2, resp.Accepted)
	require.Equal(t, 1, resp.Sampled)
	require.Zero(t, resp.Rejected)

	require.NoError(t, r.eng.FlushAll())
	got := r.storedLines()
	require.Len(t, got, 2)
	require.Equal(t, 1, got[`{"level":"error","msg":"mail [REDACTED:email] failed"}`])
	require.Equal(t, 1, got["job for [REDACTED:email] done"])
	require.NotContains(t, fmt.Sprint(got), "example.com", "redacted before the WAL, so it never reaches a chunk")
}

func TestPushRecordsMetrics(t *testing.T) {
	r := newRig(t)
	reg := metrics.NewRegistry()
	set := metrics.NewSet(reg)
	r.h.Metrics = set
	r.eng.cfg.Metrics = set
	p, _ := ParsePolicy("", "api=0")
	r.h.Policy = p

	rec := r.push(line(t0, "worker", "hello")+line(t0, "api", "dropped")+"not json\n", nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())

	var out bytes.Buffer
	reg.Write(&out)
	s := out.String()
	require.Contains(t, s, "argus_ingest_lines_total 1\n")
	require.Contains(t, s, "argus_ingest_bytes_total 5\n")
	require.Contains(t, s, "argus_ingest_rejected_lines_total 1\n")
	require.Contains(t, s, "argus_ingest_sampled_lines_total 1\n")

	require.NoError(t, r.eng.FlushAll())
	out.Reset()
	reg.Write(&out)
	require.Contains(t, out.String(), "argus_chunks_flushed_total 1\n")
}
