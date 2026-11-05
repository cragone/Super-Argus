package query

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func process(t *testing.T, src string, labels map[string]string, line string) (map[string]string, bool) {
	t.Helper()
	q, err := Parse(src)
	require.NoError(t, err)
	p, err := Compile(q)
	require.NoError(t, err)
	return p.Process(labels, line)
}

func TestMatchLabels(t *testing.T) {
	cases := []struct {
		q      string
		labels map[string]string
		want   bool
	}{
		{`{a="x"}`, map[string]string{"a": "x"}, true},
		{`{a="x"}`, map[string]string{"a": "y"}, false},
		{`{a="x", b!="z"}`, map[string]string{"a": "x"}, true}, // missing label counts as ""
		{`{a="x", b!="z"}`, map[string]string{"a": "x", "b": "z"}, false},
		{`{a=~"x|y"}`, map[string]string{"a": "y"}, true},
		{`{a=~"x"}`, map[string]string{"a": "xx"}, false}, // regex is anchored
		{`{a="x", b!~"q.*"}`, map[string]string{"a": "x", "b": "qux"}, false},
		{`{a="x", b=~".*"}`, map[string]string{"a": "x"}, true},
	}
	for _, c := range cases {
		q, err := Parse(c.q)
		require.NoError(t, err, c.q)
		p, _ := Compile(q)
		require.Equal(t, c.want, p.MatchLabels(c.labels), c.q)
	}
}

func TestLineFilters(t *testing.T) {
	l := map[string]string{"a": "x"}
	_, ok := process(t, `{a="x"} |= "time" != "healthcheck"`, l, "request timeout")
	require.True(t, ok)
	_, ok = process(t, `{a="x"} |= "time" != "healthcheck"`, l, "healthcheck timeout")
	require.False(t, ok)
	_, ok = process(t, `{a="x"} |~ "5\\d\\d" !~ "GET /ping"`, l, "POST /x 503")
	require.True(t, ok)
	_, ok = process(t, `{a="x"} |~ "5\\d\\d" !~ "GET /ping"`, l, "GET /ping 503")
	require.False(t, ok)
}

func TestJSONParserAndFieldFilters(t *testing.T) {
	l := map[string]string{"service": "api"}
	line := `{"level":"error","status":503,"latency":0.5,"ok":false,"req":{"id":"r1","path":"/v1/x"},"tags":["a","b"],"n":null}`

	f, ok := process(t, `{service="api"} | json`, l, line)
	require.True(t, ok)
	require.Equal(t, map[string]string{"level": "error", "status": "503", "latency": "0.5", "ok": "false",
		"req_id": "r1", "req_path": "/v1/x", "tags": `["a","b"]`, "n": ""}, f)

	for q, want := range map[string]bool{
		`{service="api"} | json | level="error"`:              true,
		`{service="api"} | json | level="warn"`:               false,
		`{service="api"} | json | level!="warn"`:              true,
		`{service="api"} | json | status>=500`:                true,
		`{service="api"} | json | status>503`:                 false,
		`{service="api"} | json | status=503`:                 true,
		`{service="api"} | json | latency<1`:                  true,
		`{service="api"} | json | latency<=0.5`:               true,
		`{service="api"} | json | req_path=~"/v1/.*"`:         true,
		`{service="api"} | json | req_path!~"/v1/.*"`:         false,
		`{service="api"} | json | missing!="x"`:               true, // missing behaves as ""
		`{service="api"} | json | missing="x"`:                false,
		`{service="api"} | json | missing>1`:                  false,
		`{service="api"} | json | level>1`:                    false, // non-numeric
		`{service="api"} | level="error"`:                     false, // no parser stage
		`{service="api"} | service="api"`:                     true,  // stream labels are visible to filters
		`{service="api"} | json | level="error" | status<500`: false,
	} {
		_, ok := process(t, q, l, line)
		require.Equal(t, want, ok, q)
	}

	// invalid JSON: the line is kept with no fields, field filters on it fail
	f, ok = process(t, `{service="api"} | json`, l, "plain text")
	require.True(t, ok)
	require.Empty(t, f)
	_, ok = process(t, `{service="api"} | json | level="error"`, l, "plain text")
	require.False(t, ok)
	f, _ = process(t, `{service="api"} | json`, l, `["not","object"]`)
	require.Empty(t, f)
}

func TestLogfmtParser(t *testing.T) {
	l := map[string]string{"service": "api"}
	line := `ts=2026-01-01 level=error msg="request failed: bad \"gateway\"" status=502 dur=1.5s flag key-with-dash=v`
	f, ok := process(t, `{service="api"} | logfmt`, l, line)
	require.True(t, ok)
	require.Equal(t, map[string]string{"ts": "2026-01-01", "level": "error", "msg": `request failed: bad "gateway"`,
		"status": "502", "dur": "1.5s", "flag": "", "key_with_dash": "v"}, f)

	_, ok = process(t, `{service="api"} | logfmt | level="error" | status>=500`, l, line)
	require.True(t, ok)
	_, ok = process(t, `{service="api"} | logfmt | msg=~".*gateway.*"`, l, line)
	require.True(t, ok)

	f, _ = process(t, `{service="api"} | logfmt`, l, `a="unterminated b=2`)
	require.Equal(t, "unterminated b=2", f["a"])
	f, _ = process(t, `{service="api"} | logfmt`, l, `=x  y=1`)
	require.Equal(t, "1", f["y"])
}
