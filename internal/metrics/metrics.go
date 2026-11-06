// Package metrics is a small Prometheus-compatible registry: counters, gauges
// and histograms rendered in the text exposition format, with no dependencies.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Registry holds metrics and renders them.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
	order    []string
}

type family struct {
	name, help, typ string
	series          []series
}

type series interface {
	write(w io.Writer, name string)
	labels() string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{families: map[string]*family{}} }

func (r *Registry) add(name, help, typ string, s series) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.families[name]
	if f == nil {
		f = &family{name: name, help: help, typ: typ}
		r.families[name] = f
		r.order = append(r.order, name)
	}
	if f.typ != typ {
		panic("metrics: " + name + " registered with two types")
	}
	f.series = append(f.series, s)
}

// labelString renders key,value pairs as {k="v",...}; "" for none.
func labelString(kv []string) string {
	if len(kv) == 0 {
		return ""
	}
	if len(kv)%2 != 0 {
		panic("metrics: odd label list")
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `%s="%s"`, kv[i], escape(kv[i+1]))
	}
	b.WriteByte('}')
	return b.String()
}

func escape(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

// Counter is a monotonically increasing value.
type Counter struct {
	v  atomic.Uint64
	lb string
}

// Counter registers a counter. labels are optional key,value pairs.
func (r *Registry) Counter(name, help string, labels ...string) *Counter {
	c := &Counter{lb: labelString(labels)}
	r.add(name, help, "counter", c)
	return c
}

// Add increases the counter by n (negative values are ignored).
func (c *Counter) Add(n int) {
	if n > 0 {
		c.v.Add(uint64(n))
	}
}

// Inc adds one.
func (c *Counter) Inc() { c.v.Add(1) }

// Value returns the current count.
func (c *Counter) Value() uint64 { return c.v.Load() }

func (c *Counter) labels() string { return c.lb }
func (c *Counter) write(w io.Writer, name string) {
	fmt.Fprintf(w, "%s%s %d\n", name, c.lb, c.v.Load())
}

type gauge struct {
	fn func() float64
	lb string
}

// Gauge registers a gauge read from fn at scrape time.
func (r *Registry) Gauge(name, help string, fn func() float64, labels ...string) {
	r.add(name, help, "gauge", &gauge{fn: fn, lb: labelString(labels)})
}

func (g *gauge) labels() string { return g.lb }
func (g *gauge) write(w io.Writer, name string) {
	fmt.Fprintf(w, "%s%s %s\n", name, g.lb, fmtFloat(g.fn()))
}

// DefaultBuckets suit request and flush latencies in seconds.
var DefaultBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}

// Histogram counts observations into cumulative buckets.
type Histogram struct {
	bounds []float64
	lb     string
	mu     sync.Mutex
	counts []uint64 // per bucket, plus +Inf at the end
	sum    float64
	count  uint64
}

// Histogram registers a histogram. buckets must be ascending upper bounds.
func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *Histogram {
	if len(buckets) == 0 {
		buckets = DefaultBuckets
	}
	h := &Histogram{bounds: buckets, lb: labelString(labels), counts: make([]uint64, len(buckets)+1)}
	r.add(name, help, "histogram", h)
	return h
}

// Observe records v.
func (h *Histogram) Observe(v float64) {
	i := sort.SearchFloat64s(h.bounds, v) // first bound >= v
	h.mu.Lock()
	h.counts[i]++
	h.sum += v
	h.count++
	h.mu.Unlock()
}

// ObserveDuration records the time since start in seconds.
func (h *Histogram) ObserveDuration(start time.Time) { h.Observe(time.Since(start).Seconds()) }

func (h *Histogram) labels() string { return h.lb }
func (h *Histogram) write(w io.Writer, name string) {
	h.mu.Lock()
	counts := append([]uint64(nil), h.counts...)
	sum, count := h.sum, h.count
	h.mu.Unlock()
	le := func(v string) string {
		if h.lb == "" {
			return fmt.Sprintf(`{le="%s"}`, v)
		}
		return h.lb[:len(h.lb)-1] + fmt.Sprintf(`,le="%s"}`, v)
	}
	var cum uint64
	for i, b := range h.bounds {
		cum += counts[i]
		fmt.Fprintf(w, "%s_bucket%s %d\n", name, le(fmtFloat(b)), cum)
	}
	cum += counts[len(h.bounds)]
	fmt.Fprintf(w, "%s_bucket%s %d\n", name, le("+Inf"), cum)
	fmt.Fprintf(w, "%s_sum%s %s\n", name, h.lb, fmtFloat(sum))
	fmt.Fprintf(w, "%s_count%s %d\n", name, h.lb, count)
}

func fmtFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return fmt.Sprintf("%g", v)
}

// Write renders every metric in registration order.
func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	fams := make([]*family, 0, len(r.order))
	for _, n := range r.order {
		f := *r.families[n]
		f.series = append([]series(nil), f.series...)
		fams = append(fams, &f)
	}
	r.mu.Unlock()
	for _, f := range fams {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, strings.ReplaceAll(f.help, "\n", " "), f.name, f.typ)
		for _, s := range f.series {
			s.write(w, f.name)
		}
	}
}
