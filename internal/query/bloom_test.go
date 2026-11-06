package query

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/stretchr/testify/require"
)

// A rare substring filter must skip most blocks via the chunk bloom filters
// while returning exactly what an unoptimised scan returns.
func TestLineFilterUsesBloomWithIdenticalResults(t *testing.T) {
	old := chunk.TargetBlockBytes
	chunk.TargetBlockBytes = 8 << 10 // many small blocks in one chunk
	t.Cleanup(func() { chunk.TargetBlockBytes = old })

	f := newFixture(t)
	id := f.stream("api", "prod")
	const n = 6000
	es := make([]chunk.Entry, n)
	for i := range es {
		line := fmt.Sprintf(`level=info method=GET path=/api/v1/items/%d status=200 msg="request handled"`, i%977)
		if i%1500 == 0 {
			line += " upstream-timeout"
		}
		es[i] = chunk.Entry{TS: base.UnixNano() + int64(i)*int64(time.Millisecond), Line: line}
	}
	f.addChunk(id, es)

	run := func(q string) *Result {
		res, err := f.svc.Range(context.Background(), "u", Request{Query: q, Start: base.UnixNano() - 1, End: base.Add(time.Minute).UnixNano(), Limit: 5000})
		require.NoError(t, err)
		return res
	}
	fast := run(`{service="api"} |= "upstream-timeout"`)
	slow := run(`{service="api"} |~ "upstream-timeout"`) // regex filters cannot use the bloom

	require.Len(t, fast.Entries, 4)
	require.Equal(t, len(slow.Entries), len(fast.Entries))
	for i := range fast.Entries {
		require.Equal(t, slow.Entries[i].TS, fast.Entries[i].TS)
		require.Equal(t, slow.Entries[i].Line, fast.Entries[i].Line)
	}
	require.EqualValues(t, n, slow.Stats.LinesScanned, "regex scans everything")
	require.Less(t, fast.Stats.LinesScanned, slow.Stats.LinesScanned/4,
		"bloom skipped most blocks: scanned %d of %d lines", fast.Stats.LinesScanned, n)

	// Metric queries share the optimisation and must agree too.
	m := func(q string) float64 {
		res, err := f.svc.Metric(context.Background(), "u", MetricRequest{Query: q, Start: base.UnixNano(), End: base.Add(10 * time.Second).UnixNano(), Step: 10 * time.Second})
		require.NoError(t, err)
		sum := 0.0
		for _, s := range res.Series {
			for _, p := range s.Points {
				sum += p.V
			}
		}
		return sum
	}
	require.Equal(t, m(`count_over_time({service="api"} |~ "upstream-timeout" [10s])`), m(`count_over_time({service="api"} |= "upstream-timeout" [10s])`))
}
