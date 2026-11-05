package query

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/stretchr/testify/require"
)

var metricParseCases = []string{
	`count_over_time({service="api"}[5m])`,
	`count_over_time({service="api"} |= "error" [5m])`,
	`rate({service="api"} | json | status>=500 [1h])`,
	`bytes_over_time({service="api"}[30s])`,
	`rate({service="api"}[250ms])`,
	`rate({service="api"}[1.5h])`,
	`rate({service="api"}[2d])`,
	`sum(count_over_time({env="prod"}[1m]))`,
	`sum by (level) (count_over_time({env="prod"}[1m]))`,
	`sum by (level, route) (rate({env="prod"} | json [1m]))`,
	`sum(rate({env="prod"}[1m])) by (level)`,
	`topk(10, sum by (route) (count_over_time({service="api"} | json | status>=500 [1h])))`,
	`topk(3, rate({service="api"}[5m]))`,
	// errors
	`rate({service="api"})`,
	`rate({service="api"}[5m]`,
	`rate({service="api"}[5])`,
	`rate({service="api"}[5x])`,
	`rate({service="api"}[0m])`,
	`rate(service="api"[5m])`,
	`rate({service=~".*"}[5m])`,
	`avg_over_time({service="api"}[5m])`,
	`sum by () (rate({a="b"}[1m]))`,
	`sum by (a) (rate({a="b"}[1m])) by (b)`,
	`sum(topk(2, rate({a="b"}[1m])))`,
	`sum({a="b"})`,
	`topk(0, rate({a="b"}[1m]))`,
	`topk(1.5, rate({a="b"}[1m]))`,
	`topk(2 rate({a="b"}[1m]))`,
	`topk(2, topk(3, rate({a="b"}[1m])))`,
	`rate({a="b"}[1m]) extra`,
	`{service="api"}`,
	``,
	`rate({a="b"} | json | level="x" [1m]) trailing`,
}

func TestMetricParseGolden(t *testing.T) {
	var b strings.Builder
	for _, src := range metricParseCases {
		b.WriteString("query: " + src + "\n")
		e, err := ParseMetric(src)
		if err != nil {
			b.WriteString("error: " + err.Error() + "\n\n")
			continue
		}
		b.WriteString("canon: " + e.String() + "\n")
		e2, err := ParseMetric(e.String())
		require.NoError(t, err, src)
		require.Equal(t, e.String(), e2.String(), "canonical form must round-trip: "+src)
		b.WriteString("\n")
	}
	golden(t, "metric_parse.golden", b.String())
}

func TestLogParserStillRejectsMetricAndAcceptsLogs(t *testing.T) {
	_, err := Parse(`rate({a="b"}[1m])`)
	require.ErrorContains(t, err, "metric queries")
	_, err = Parse(`{a="b"} |= "x" | json`)
	require.NoError(t, err)
	require.True(t, IsMetric(`rate({a="b"}[1m])`))
	require.False(t, IsMetric(`{a="b"}`))
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"250ms": 250 * time.Millisecond, "30s": 30 * time.Second, "5m": 5 * time.Minute,
		"1h": time.Hour, "2d": 48 * time.Hour, "1w": 7 * 24 * time.Hour, "1.5h": 90 * time.Minute,
	} {
		got, err := ParseDuration(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "5", "m", "0s", "-5m", "5x", "1y", "5 m"} {
		_, err := ParseDuration(bad)
		require.Error(t, err, bad)
	}
}

// --- evaluator ---

func (f *fixture) metric(q string, start, end int, step time.Duration) *MetricResult {
	f.t.Helper()
	res, err := f.svc.Metric(context.Background(), "u1", MetricRequest{Query: q, Start: at(start), End: at(end), Step: step})
	require.NoError(f.t, err, q)
	return res
}

func seriesMap(res *MetricResult) map[string][]Point {
	out := map[string][]Point{}
	for _, s := range res.Series {
		keys := make([]string, 0, len(s.Labels))
		for k, v := range s.Labels {
			keys = append(keys, k+"="+v)
		}
		sort.Strings(keys)
		out[strings.Join(keys, ",")] = s.Points
	}
	return out
}

// bruteForce evaluates a range aggregation directly over the generated lines.
func (f *fixture) bruteForce(t *testing.T, q string, start, end int64, step time.Duration) map[string][]Point {
	t.Helper()
	expr, err := ParseMetric(q)
	require.NoError(t, err)
	plan, err := newMetricPlan(expr, start/int64(step)*int64(step), end/int64(step)*int64(step), step)
	require.NoError(t, err)
	root := plan.root
	out := map[string][]Point{}
	sums := map[string][]float64{}
	for i := 0; i < plan.nSteps; i++ {
		ts := plan.start + int64(i)*plan.step
		for _, l := range f.all {
			labels := map[string]string{"service": l.service, "env": l.env}
			if !plan.pipe.MatchLabels(labels) {
				continue
			}
			fields, ok := plan.pipe.Process(labels, l.e.Line)
			if !ok || l.e.TS <= ts-plan.window || l.e.TS > ts {
				continue
			}
			key, kl := plan.key(labels, fields, 0)
			_ = key
			keys := make([]string, 0, len(kl))
			for k, v := range kl {
				keys = append(keys, k+"="+v)
			}
			sort.Strings(keys)
			k := strings.Join(keys, ",")
			if sums[k] == nil {
				sums[k] = make([]float64, plan.nSteps)
			}
			v := 1.0
			if root.Func == FuncBytesOverTime {
				v = float64(len(l.e.Line))
			}
			sums[k][i] += v
		}
	}
	for k, vals := range sums {
		for i, v := range vals {
			if root.Func == FuncRate {
				v /= root.Window.Seconds()
			}
			out[k] = append(out[k], Point{T: (plan.start + int64(i)*plan.step) / 1e6, V: v})
		}
	}
	return out
}

func TestMetricMatchesBruteForce(t *testing.T) {
	f := newFixture(t)
	f.load()
	queries := []string{
		`count_over_time({service="api", env="prod"}[10s])`,
		`count_over_time({service="api", env="prod"}[25s])`, // window not a multiple of the step
		`rate({service="api", env="prod"}[20s])`,
		`bytes_over_time({service="web", env="dev"}[15s])`,
		`count_over_time({service=~"api|web"} |= "timeout" [30s])`,
		`sum(count_over_time({service=~".+"}[10s]))`,
		`sum by (service) (count_over_time({service=~".+"}[10s]))`,
		`sum by (service, env) (rate({service=~".+"}[10s]))`,
		`sum by (level) (count_over_time({service="api"} | json [10s]))`,
		`sum by (level) (count_over_time({service="web"} | logfmt | status>=500 [20s]))`,
		`sum(rate({service="api"} | json | level="error" [10s])) by (env)`,
	}
	for _, step := range []time.Duration{10 * time.Second, 7 * time.Second, 5 * time.Second} {
		for _, q := range queries {
			got := seriesMap(f.metric(q, 0, 59, step))
			want := f.bruteForce(t, q, at(0), at(59), step)
			require.Equal(t, len(want), len(got), "%s step=%s: series sets differ\n got %v\nwant %v", q, step, got, want)
			for k, wp := range want {
				require.Equal(t, wp, got[k], "%s step=%s series {%s}", q, step, k)
			}
		}
	}
}

func TestMetricKnownValues(t *testing.T) {
	f := newFixture(t)
	f.load()
	res := f.metric(`count_over_time({service="api", env="prod"}[10s])`, 0, 40, 10*time.Second)
	require.Equal(t, 10*time.Second, res.Step)
	require.Len(t, res.Series, 1)
	var vals []float64
	for _, p := range res.Series[0].Points {
		vals = append(vals, p.V)
	}
	// t=0: only line 0; afterwards one line per second -> 10 per 10s window
	require.Equal(t, []float64{1, 10, 10, 10, 10}, vals)
	require.Equal(t, at(0)/1e6, res.Series[0].Points[0].T, "timestamps are milliseconds")

	res = f.metric(`rate({service="api", env="prod"}[10s])`, 10, 40, 10*time.Second)
	require.InDelta(t, 1.0, res.Series[0].Points[1].V, 1e-9, "10 lines in 10s = 1/s")
}

func TestMetricGroupingLabels(t *testing.T) {
	f := newFixture(t)
	f.load()
	res := f.metric(`sum by (service) (count_over_time({service=~".+"}[10s]))`, 0, 30, 10*time.Second)
	m := seriesMap(res)
	require.Equal(t, []string{"service=api", "service=web", "service=worker"}, sortedKeys(m))
	require.Equal(t, 20.0, m["service=api"][1].V, "two envs x 10 lines")

	res = f.metric(`sum(count_over_time({service=~".+"}[10s]))`, 0, 30, 10*time.Second)
	require.Len(t, res.Series, 1)
	require.Empty(t, res.Series[0].Labels)
	require.Equal(t, 60.0, res.Series[0].Points[1].V)

	// without sum: one series per stream, labelled with the stream labels
	res = f.metric(`count_over_time({service="api"}[10s])`, 0, 30, 10*time.Second)
	require.Equal(t, []string{"env=dev,service=api", "env=prod,service=api"}, sortedKeys(seriesMap(res)))
}

func sortedKeys(m map[string][]Point) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func TestTopK(t *testing.T) {
	f := newFixture(t)
	f.load()
	// api lines carry status 200..700; count 5xx+ per route: distinct values per route
	res := f.metric(`topk(2, sum by (route) (count_over_time({service="api"} | json [30s])))`, 0, 59, 10*time.Second)
	require.LessOrEqual(t, len(res.Series), 5)
	byStep := map[int64]int{}
	for _, s := range res.Series {
		for _, p := range s.Points {
			byStep[p.T]++
		}
	}
	for ts, n := range byStep {
		require.LessOrEqual(t, n, 2, "at most 2 series per step (t=%d)", ts)
	}
	require.NotEmpty(t, byStep)

	// every kept point is among the 2 largest values at that step
	all := f.metric(`sum by (route) (count_over_time({service="api"} | json [30s]))`, 0, 59, 10*time.Second)
	for _, s := range res.Series {
		for _, p := range s.Points {
			larger := 0
			for _, o := range all.Series {
				for _, q := range o.Points {
					if q.T == p.T && q.V > p.V {
						larger++
					}
				}
			}
			require.Less(t, larger, 2)
		}
	}

	// topk >= series count keeps everything
	res = f.metric(`topk(100, sum by (route) (count_over_time({service="api"} | json [30s])))`, 0, 59, 10*time.Second)
	require.Equal(t, len(all.Series), len(res.Series))
}

func TestMetricIncludesHeadsWithoutDuplicates(t *testing.T) {
	f := newFixture(t)
	id := f.stream("api", "prod")
	cid := f.addChunk(id, mkSecEntries(0, 10))
	f.heads.heads = append(f.heads.heads,
		headSnap(id, "unflushed", mkSecEntries(10, 20)), // live lines
		headSnap(id, cid, mkSecEntries(0, 10)),          // snapshot of a head that already became chunk cid
	)
	res := f.metric(`sum(count_over_time({service="api"}[100s]))`, 0, 30, 10*time.Second)
	require.Equal(t, 20.0, res.Series[0].Points[3].V, "10 flushed + 10 live, nothing counted twice")
	require.Equal(t, 10, res.Stats.HeadLines)
}

func TestMetricCacheForOneStep(t *testing.T) {
	f := newFixture(t)
	f.load()
	now := time.Now()
	f.svc.Now = func() time.Time { return now }
	q := `count_over_time({service="api", env="prod"}[10s])`
	req := MetricRequest{Query: q, Start: at(0), End: at(40), Step: 10 * time.Second}

	a, err := f.svc.Metric(context.Background(), "u1", req)
	require.NoError(t, err)
	b, err := f.svc.Metric(context.Background(), "u2", req)
	require.NoError(t, err)
	require.Same(t, a, b, "second identical request is served from cache")

	// same step window after alignment: request bounds jitter inside one step
	req2 := req
	req2.Start, req2.End = req.Start+int64(3*time.Second), req.End+int64(4*time.Second)
	c, err := f.svc.Metric(context.Background(), "u1", req2)
	require.NoError(t, err)
	require.Same(t, a, c)

	now = now.Add(11 * time.Second) // step elapsed
	d, err := f.svc.Metric(context.Background(), "u1", req)
	require.NoError(t, err)
	require.NotSame(t, a, d)
	require.Equal(t, a.Series, d.Series)

	// different query or range is a different entry
	req3 := req
	req3.End = at(50)
	e, err := f.svc.Metric(context.Background(), "u1", req3)
	require.NoError(t, err)
	require.NotSame(t, d, e)
}

func TestMetricLimitsAndValidation(t *testing.T) {
	f := newFixture(t)
	f.load()
	ctx := context.Background()
	call := func(r MetricRequest) error { _, err := f.svc.Metric(ctx, "u1", r); return err }
	ok := MetricRequest{Query: `rate({service="api"}[10s])`, Start: at(0), End: at(30), Step: 10 * time.Second}

	require.ErrorIs(t, call(MetricRequest{Query: `{service="api"}`, Start: at(0), End: at(30)}), ErrBadRequest, "log query on the metric endpoint")
	require.ErrorIs(t, call(MetricRequest{Query: `rate({service="api"}[10s])`}), ErrBadRequest, "range required")
	bad := ok
	bad.End = bad.Start
	require.ErrorIs(t, call(bad), ErrBadRequest)
	bad = ok
	bad.Step = 500 * time.Millisecond
	require.ErrorIs(t, call(bad), ErrBadRequest, "step under 1s")

	f.svc.Limits.MaxSteps = 5
	bad = ok
	bad.Start, bad.End, bad.Step = at(0), at(600), 10*time.Second
	err := call(bad)
	require.ErrorIs(t, err, ErrLimit)
	require.Contains(t, err.Error(), "larger step")
	f.svc.Limits.MaxSteps = 2000

	f.svc.Limits.MaxMetricRange = time.Hour
	bad = ok
	bad.End = at(2 * 3600)
	require.ErrorIs(t, call(bad), ErrLimit)
	f.svc.Limits.MaxMetricRange = 10 * time.Second
	require.ErrorIs(t, call(ok), ErrLimit, "window larger than the allowed range")
	f.svc.Limits.MaxMetricRange = 31 * 24 * time.Hour

	f.svc.Limits.MaxSeries = 2
	err = call(MetricRequest{Query: `count_over_time({service=~".+"}[10s])`, Start: at(0), End: at(30), Step: 10 * time.Second})
	require.ErrorIs(t, err, ErrLimit)
	require.Contains(t, err.Error(), "series")
	f.svc.Limits.MaxSeries = 500

	f.svc.Limits.MaxBytesScanned = 100
	require.ErrorIs(t, call(MetricRequest{Query: `rate({service=~".+"}[10s])`, Start: at(0), End: at(30), Step: 10 * time.Second}), ErrLimit)
}

func TestChooseStep(t *testing.T) {
	require.Equal(t, time.Second, ChooseStep(0, int64(time.Minute)))
	require.Equal(t, 15*time.Second, ChooseStep(0, int64(time.Hour))) // 3600s/250 = 14.4s -> 15s
	require.Equal(t, 6*time.Minute, ChooseStep(0, int64(25*time.Hour)))
}

func TestMetricNoMatchesIsEmptyNotError(t *testing.T) {
	f := newFixture(t)
	f.load()
	res := f.metric(`rate({service="nope"}[10s])`, 0, 30, 10*time.Second)
	require.Empty(t, res.Series)
	res = f.metric(`rate({service="api"} |= "never-appears" [10s])`, 0, 30, 10*time.Second)
	require.Empty(t, res.Series)
	_ = fmt.Sprint(res)
}

// mkSecEntries makes one line per second for seconds [from, to).
func mkSecEntries(from, to int) []chunk.Entry {
	var es []chunk.Entry
	for i := from; i < to; i++ {
		es = append(es, chunk.Entry{TS: at(i), Line: fmt.Sprintf("line %d", i)})
	}
	return es
}

func headSnap(id uint64, chunkID string, es []chunk.Entry) chunk.HeadSnapshot {
	return chunk.HeadSnapshot{StreamID: id, ChunkID: chunkID, Entries: es}
}
