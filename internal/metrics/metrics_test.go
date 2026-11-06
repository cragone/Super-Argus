package metrics

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func render(r *Registry) string {
	var b bytes.Buffer
	r.Write(&b)
	return b.String()
}

func TestExposition(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("x_total", "A counter.")
	c.Inc()
	c.Add(4)
	c.Add(-3) // ignored
	r.Gauge("x_gauge", "A gauge.", func() float64 { return 2.5 })
	a := r.Counter("x_errors_total", "Errors.", "kind", "log")
	b := r.Counter("x_errors_total", "Errors.", "kind", `we"ird`)
	a.Inc()
	b.Add(2)
	h := r.Histogram("x_seconds", "Latency.", []float64{0.1, 1}, "kind", "log")
	h.Observe(0.05)
	h.Observe(0.5)
	h.Observe(7)

	require.Equal(t, `# HELP x_total A counter.
# TYPE x_total counter
x_total 5
# HELP x_gauge A gauge.
# TYPE x_gauge gauge
x_gauge 2.5
# HELP x_errors_total Errors.
# TYPE x_errors_total counter
x_errors_total{kind="log"} 1
x_errors_total{kind="we\"ird"} 2
# HELP x_seconds Latency.
# TYPE x_seconds histogram
x_seconds_bucket{kind="log",le="0.1"} 1
x_seconds_bucket{kind="log",le="1"} 2
x_seconds_bucket{kind="log",le="+Inf"} 3
x_seconds_sum{kind="log"} 7.55
x_seconds_count{kind="log"} 3
`, render(r))
}

func TestHistogramWithoutLabels(t *testing.T) {
	r := NewRegistry()
	r.Histogram("h", "h", []float64{1}).Observe(0.5)
	require.Contains(t, render(r), `h_bucket{le="1"} 1`)
	require.Contains(t, render(r), "h_count 1\n")
}

func TestTypeConflictPanics(t *testing.T) {
	r := NewRegistry()
	r.Counter("m", "m")
	require.Panics(t, func() { r.Gauge("m", "m", func() float64 { return 0 }) })
}

func TestNilSetIsSafe(t *testing.T) {
	var s *Set
	s.Accepted(1, 1)
	s.Rejected(1)
	s.Sampled(1)
	s.RateLimited()
	s.Flush(1, time.Second, nil)
	s.Query("log", time.Second, nil)
}

func TestSetRecords(t *testing.T) {
	r := NewRegistry()
	s := NewSet(r)
	s.Accepted(10, 100)
	s.Query("metric", 50*time.Millisecond, nil)
	s.Query("metric", time.Second, assertErr{})
	s.Query("bogus", time.Second, nil) // unknown kind is ignored, not a panic
	s.Flush(2, time.Second, nil)
	out := render(r)
	require.Contains(t, out, "argus_ingest_lines_total 10\n")
	require.Contains(t, out, "argus_ingest_bytes_total 100\n")
	require.Contains(t, out, `argus_query_errors_total{kind="metric"} 1`)
	require.Contains(t, out, `argus_query_duration_seconds_count{kind="metric"} 2`)
	require.Contains(t, out, "argus_chunks_flushed_total 2\n")
}

type assertErr struct{}

func (assertErr) Error() string { return "boom" }
