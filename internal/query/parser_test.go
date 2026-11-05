package query

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.WriteFile(p, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(p)
	require.NoError(t, err, "missing golden file; run with -update")
	require.Equal(t, strings.ReplaceAll(string(want), "\r\n", "\n"), got)
}

var parseCases = []string{
	`{service="api"}`,
	`{service="api", env="prod"}`,
	`{service="api",}`,
	`{service=~"api|web", env!="dev"}`,
	"{service=`a\"b`}",
	`{service="api"} |= "timeout"`,
	`{service="api"} |= "timeout" != "healthcheck" |~ "5\\d\\d" !~ "GET /ping"`,
	`{service="api"} | json`,
	`{service="api"} | logfmt | level="error"`,
	`{service="api"} | json | level="error" | status>=500 | latency<0.25`,
	`{service="api"} | json | route=~"/v1/.*" | user!="bot" | code=200 | code!=404 | n>-3`,
	`{service="api"} |= "a" | json | msg !~ "x.y"`,
	`{service="api"} | json = "x"`,
	// errors
	``,
	`{}`,
	`{service=~".*"}`,
	`{service!="x"}`,
	`{service=""}`,
	`{service="api"`,
	`{service="api}`,
	`{service=api}`,
	`{service "api"}`,
	`{service=~"("}`,
	`{service="api"} |= `,
	`{service="api"} |= 5`,
	`{service="api"} |~ "("`,
	`{service="api"} | foo`,
	`{service="api"} | level>"x"`,
	`{service="api"} | level=~5`,
	`{service="api"} | level=`,
	`{service="api"} garbage`,
	`{service="api"} ! "x"`,
	`count_over_time({service="api"}[5m])`,
	`{service="api"} # comment`,
}

func TestParseGolden(t *testing.T) {
	var b strings.Builder
	for _, src := range parseCases {
		b.WriteString("query: " + src + "\n")
		q, err := Parse(src)
		if err != nil {
			b.WriteString("error: " + err.Error() + "\n\n")
			continue
		}
		b.WriteString("canon: " + q.String() + "\n")
		// canonical form must round-trip to itself
		q2, err := Parse(q.String())
		require.NoError(t, err, src)
		require.Equal(t, q.String(), q2.String(), src)
		b.WriteString("\n")
	}
	golden(t, "parse.golden", b.String())
}
