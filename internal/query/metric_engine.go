package query

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/models"
)

// MetricRequest is a range metric query. Start and End are unix nanoseconds
// (End inclusive); Step is the resolution (0 = pick automatically).
type MetricRequest struct {
	Query string
	Start int64
	End   int64
	Step  time.Duration
}

// Point is one sample; T is unix milliseconds.
type Point struct {
	T int64
	V float64
}

// Series is one labelled line of a metric result.
type Series struct {
	Labels map[string]string
	Points []Point
}

// MetricResult is the answer to a metric query. Start/End are the step-aligned
// bounds actually evaluated.
type MetricResult struct {
	Series []Series
	Step   time.Duration
	Start  int64
	End    int64
	Stats  Stats
}

// ChooseStep returns a step giving roughly 250 points, at least one second.
func ChooseStep(start, end int64) time.Duration {
	d := time.Duration(end-start) / 250
	if d < time.Second {
		return time.Second
	}
	return ((d + time.Second - 1) / time.Second) * time.Second
}

type seriesAcc struct {
	labels map[string]string
	diff   []float64 // difference array over steps (len nSteps+1)
}

type metricPlan struct {
	root    *RangeAgg
	sum     *SumAgg
	top     *TopK
	pipe    *Pipeline
	start   int64
	step    int64
	nSteps  int
	window  int64
	perByte bool // bytes_over_time: weight each line by its size
}

func newMetricPlan(expr MetricExpr, start, end int64, step time.Duration) (*metricPlan, error) {
	p := &metricPlan{start: start, step: int64(step), nSteps: int((end-start)/int64(step)) + 1}
	switch e := expr.(type) {
	case *TopK:
		p.top = e
		expr = e.Inner
	}
	if s, ok := expr.(*SumAgg); ok {
		p.sum = s
		expr = s.Inner
	}
	root, ok := expr.(*RangeAgg)
	if !ok {
		return nil, badf("expected a range aggregation such as rate(...)")
	}
	p.root = root
	p.window = int64(root.Window)
	p.perByte = root.Func == FuncBytesOverTime
	pipe, err := Compile(root.Query)
	if err != nil {
		return nil, &Error{ErrBadRequest, err.Error()}
	}
	p.pipe = pipe
	return p, nil
}

// key returns the series key and labels for a line.
func (p *metricPlan) key(streamLabels, fields map[string]string, streamID uint64) (string, map[string]string) {
	if p.sum == nil {
		return FormatStreamID(streamID), streamLabels
	}
	if !p.sum.HasBy {
		return "", map[string]string{}
	}
	var b strings.Builder
	labels := make(map[string]string, len(p.sum.By))
	for _, l := range p.sum.By {
		v, ok := fields[l]
		if !ok {
			v = streamLabels[l]
		}
		b.WriteString(v)
		b.WriteByte(0xff)
		if v != "" {
			labels[l] = v
		}
	}
	return b.String(), labels
}

// add folds one line into acc by marking the range of steps whose trailing
// window (t-window, t] contains ts.
func (p *metricPlan) add(acc map[string]*seriesAcc, key string, labels map[string]string, ts int64, line string, maxSeries int) error {
	first := ceilDiv(ts-p.start, p.step)
	if first < 0 {
		first = 0
	}
	last := ceilDiv(ts+p.window-p.start, p.step) - 1
	if last >= int64(p.nSteps) {
		last = int64(p.nSteps) - 1
	}
	if first > last {
		return nil
	}
	a := acc[key]
	if a == nil {
		if maxSeries > 0 && len(acc) >= maxSeries {
			return &Error{ErrLimit, fmt.Sprintf("query produces more than %d series; aggregate with sum by (...) or narrow the selector", maxSeries)}
		}
		a = &seriesAcc{labels: labels, diff: make([]float64, p.nSteps+1)}
		acc[key] = a
	}
	v := 1.0
	if p.perByte {
		v = float64(len(line))
	}
	a.diff[first] += v
	a.diff[last+1] -= v
	return nil
}

func ceilDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a > 0) == (b > 0) {
		q++
	}
	return q
}

// Metric evaluates a metric query over [Start, End] at Step resolution.
// Results are cached for one step, so dashboards refreshing faster than the
// step do not rescan.
func (s *Service) Metric(ctx context.Context, user string, req MetricRequest) (*MetricResult, error) {
	began := time.Now()
	expr, err := ParseMetric(req.Query)
	if err != nil {
		return nil, &Error{ErrBadRequest, err.Error()}
	}
	if req.Start <= 0 || req.End <= 0 || req.End <= req.Start {
		return nil, badf("start and end are required and end must be after start")
	}
	step := req.Step
	if step <= 0 {
		step = ChooseStep(req.Start, req.End)
	}
	if step < time.Second {
		return nil, badf("step must be at least 1s")
	}
	// Align to the step so repeated requests inside one step share a cache entry.
	st := int64(step)
	start, end := req.Start/st*st, req.End/st*st
	if end <= start {
		end = start + st
	}
	lim := s.Limits
	if lim.MaxMetricRange > 0 && time.Duration(end-start) > lim.MaxMetricRange {
		return nil, &Error{ErrLimit, fmt.Sprintf("time range exceeds the maximum of %s for metric queries", lim.MaxMetricRange)}
	}
	if n := (end-start)/st + 1; lim.MaxSteps > 0 && n > int64(lim.MaxSteps) {
		return nil, &Error{ErrLimit, fmt.Sprintf("%d steps requested, maximum is %d; use a larger step", n, lim.MaxSteps)}
	}
	plan, err := newMetricPlan(expr, start, end, step)
	if err != nil {
		return nil, err
	}
	if lim.MaxMetricRange > 0 && time.Duration(plan.window) > lim.MaxMetricRange {
		return nil, &Error{ErrLimit, fmt.Sprintf("range window exceeds the maximum of %s", lim.MaxMetricRange)}
	}

	cacheKey := fmt.Sprintf("%s|%d|%d|%d", expr.String(), start, end, st)
	if res, ok := s.mcache.get(cacheKey, s.Now()); ok {
		return res, nil
	}

	release, err := s.acquire(user)
	if err != nil {
		return nil, err
	}
	defer release()
	if lim.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, lim.Timeout)
		defer cancel()
	}

	acc, stats, err := s.evalMetric(ctx, plan, start, end)
	if err != nil {
		return nil, wrapCtx(ctx, err)
	}
	res := &MetricResult{Step: step, Start: start, End: end, Stats: stats, Series: plan.finish(acc)}
	res.Stats.ElapsedMS = time.Since(began).Milliseconds()
	ttl := min(max(step, time.Second), 5*time.Minute)
	s.mcache.put(cacheKey, res, s.Now().Add(ttl))
	return res, nil
}

func (s *Service) evalMetric(ctx context.Context, p *metricPlan, start, end int64) (map[string]*seriesAcc, Stats, error) {
	var st statsAcc
	// Lines with ts in (start-window, end] matter.
	from, to := start-p.window+1, end+1
	streams, err := s.resolveStreams(ctx, p.root.Query, p.pipe)
	if err != nil {
		return nil, Stats{}, err
	}
	if len(streams) == 0 {
		return map[string]*seriesAcc{}, Stats{}, nil
	}
	ids := make(map[uint64]struct{}, len(streams))
	idList := make([]uint64, 0, len(streams))
	for id := range streams {
		ids[id] = struct{}{}
		idList = append(idList, id)
	}
	var heads []chunk.HeadSnapshot
	if s.Heads != nil {
		heads = s.Heads.SnapshotHeads(ids, from, to)
	}
	chunks, err := s.listChunks(ctx, idList, Request{Start: from, End: to})
	if err != nil {
		return nil, Stats{}, err
	}
	listed := make(map[string]struct{}, len(chunks))
	for _, c := range chunks {
		listed[c.ID] = struct{}{}
	}

	maxBytes := s.Limits.MaxBytesScanned
	maxSeries := s.Limits.MaxSeries
	line := func(acc map[string]*seriesAcc, stream uint64, e chunk.Entry) error {
		n := st.lines.Add(1)
		b := st.bytes.Add(int64(len(e.Line)))
		if n&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if maxBytes > 0 && b > maxBytes {
			return &Error{ErrLimit, fmt.Sprintf("query scanned more than %d bytes; narrow the time range or add stricter matchers", maxBytes)}
		}
		labels := streams[stream]
		fields, ok := p.pipe.Process(labels, e.Line)
		if !ok {
			return nil
		}
		key, kl := p.key(labels, fields, stream)
		return p.add(acc, key, kl, e.TS, e.Line, maxSeries)
	}

	workers := s.Limits.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	locals := make([]map[string]*seriesAcc, workers+1)
	for i := range locals {
		locals[i] = map[string]*seriesAcc{}
	}
	// Heads first, into the extra accumulator.
	for _, h := range heads {
		if _, dup := listed[h.ChunkID]; dup {
			continue
		}
		for _, e := range h.Entries {
			if err := line(locals[workers], h.StreamID, e); err != nil {
				return nil, Stats{}, err
			}
			st.headLines.Add(1)
		}
	}

	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan models.Chunk)
	var wg sync.WaitGroup
	var firstErr error
	var once sync.Once
	fail := func(err error) { once.Do(func() { firstErr = err; cancel() }) }
	var scanned atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(acc map[string]*seriesAcc) {
			defer wg.Done()
			for c := range work {
				if ctx2.Err() != nil {
					continue
				}
				data, err := s.Store.Get(c.Path)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					fail(err)
					continue
				}
				r, err := chunk.NewReader(data)
				if err != nil {
					fail(fmt.Errorf("chunk %s: %w", c.ID, err))
					continue
				}
				var lineErr error
				err = r.ScanContaining(from, to, p.pipe.Needles(), func(e chunk.Entry) bool {
					lineErr = line(acc, c.StreamID, e)
					return lineErr == nil
				})
				switch {
				case lineErr != nil:
					fail(lineErr)
				case err != nil:
					fail(fmt.Errorf("chunk %s: %w", c.ID, err))
				default:
					scanned.Add(1)
				}
			}
		}(locals[w])
	}
feed:
	for _, c := range chunks {
		select {
		case work <- c:
		case <-ctx2.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return nil, Stats{}, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, Stats{}, err
	}

	merged := locals[workers]
	for _, l := range locals[:workers] {
		for k, a := range l {
			m := merged[k]
			if m == nil {
				if maxSeries > 0 && len(merged) >= maxSeries {
					return nil, Stats{}, &Error{ErrLimit, fmt.Sprintf("query produces more than %d series; aggregate with sum by (...) or narrow the selector", maxSeries)}
				}
				merged[k] = a
				continue
			}
			for i, v := range a.diff {
				m.diff[i] += v
			}
		}
	}
	stats := st.snapshot(len(streams), 0)
	stats.ChunksScanned = int(scanned.Load())
	return merged, stats, nil
}

// finish turns difference arrays into points, applies rate() and topk().
func (p *metricPlan) finish(acc map[string]*seriesAcc) []Series {
	keys := make([]string, 0, len(acc))
	for k := range acc {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	divisor := 1.0
	if p.root.Func == FuncRate {
		divisor = p.root.Window.Seconds()
	}
	type row struct {
		key  string
		vals []float64
	}
	rows := make([]row, 0, len(keys))
	for _, k := range keys {
		a := acc[k]
		vals := make([]float64, p.nSteps)
		var run float64
		for i := 0; i < p.nSteps; i++ {
			run += a.diff[i]
			if run < 0 { // float noise
				run = 0
			}
			vals[i] = run / divisor
		}
		rows = append(rows, row{k, vals})
	}

	keep := make([][]bool, len(rows)) // per series, per step
	for i := range keep {
		keep[i] = make([]bool, p.nSteps)
		for j := range keep[i] {
			keep[i][j] = true
		}
	}
	if p.top != nil && len(rows) > p.top.K {
		for i := range keep {
			for j := range keep[i] {
				keep[i][j] = false
			}
		}
		idx := make([]int, len(rows))
		for step := 0; step < p.nSteps; step++ {
			for i := range idx {
				idx[i] = i
			}
			sort.SliceStable(idx, func(a, b int) bool { return rows[idx[a]].vals[step] > rows[idx[b]].vals[step] })
			for _, i := range idx[:p.top.K] {
				keep[i][step] = true
			}
		}
	}

	out := make([]Series, 0, len(rows))
	for i, r := range rows {
		s := Series{Labels: acc[r.key].labels}
		if s.Labels == nil {
			s.Labels = map[string]string{}
		}
		for j, v := range r.vals {
			if keep[i][j] {
				s.Points = append(s.Points, Point{T: (p.start + int64(j)*p.step) / 1e6, V: v})
			}
		}
		if len(s.Points) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// --- result cache ---

type metricCache struct {
	mu  sync.Mutex
	max int
	m   map[string]cacheEntry
}

type cacheEntry struct {
	res *MetricResult
	exp time.Time
}

func newMetricCache(max int) *metricCache { return &metricCache{max: max, m: map[string]cacheEntry{}} }

func (c *metricCache) get(key string, now time.Time) (*MetricResult, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || !now.Before(e.exp) {
		delete(c.m, key)
		return nil, false
	}
	return e.res, true
}

func (c *metricCache) put(key string, res *MetricResult, exp time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		var oldest string
		var oldestExp time.Time
		for k, e := range c.m {
			if oldest == "" || e.exp.Before(oldestExp) {
				oldest, oldestExp = k, e.exp
			}
		}
		delete(c.m, oldest)
	}
	c.m[key] = cacheEntry{res, exp}
}
