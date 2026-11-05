package query

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Range aggregation functions.
const (
	FuncCountOverTime = "count_over_time"
	FuncRate          = "rate"
	FuncBytesOverTime = "bytes_over_time"
)

// MetricExpr is a parsed metric query: a range aggregation, optionally wrapped
// in sum and/or topk.
type MetricExpr interface {
	// String renders canonical ArgusQL.
	String() string
}

// RangeAgg is func(<log query> [window]): a value per series per step computed
// from the lines in the trailing window.
type RangeAgg struct {
	Func   string
	Query  *Query
	Window time.Duration
	// WindowText is the window as written (for the canonical form).
	WindowText string
}

// SumAgg is sum [by (labels)] (inner). Without By everything folds into one series.
type SumAgg struct {
	By    []string
	HasBy bool
	Inner MetricExpr
}

// TopK keeps the K largest series at every step.
type TopK struct {
	K     int
	Inner MetricExpr
}

func (r *RangeAgg) String() string {
	return fmt.Sprintf("%s(%s [%s])", r.Func, r.Query.String(), r.WindowText)
}

func (s *SumAgg) String() string {
	if s.HasBy {
		return fmt.Sprintf("sum by (%s) (%s)", strings.Join(s.By, ", "), s.Inner.String())
	}
	return fmt.Sprintf("sum(%s)", s.Inner.String())
}

func (t *TopK) String() string { return fmt.Sprintf("topk(%d, %s)", t.K, t.Inner.String()) }

// Root returns the range aggregation at the core of the expression.
func Root(e MetricExpr) *RangeAgg {
	for {
		switch x := e.(type) {
		case *RangeAgg:
			return x
		case *SumAgg:
			e = x.Inner
		case *TopK:
			e = x.Inner
		default:
			return nil
		}
	}
}

// ParseDuration parses 250ms, 30s, 5m, 1h, 2d, 1w (decimals allowed, e.g. 1.5h).
func ParseDuration(s string) (time.Duration, error) {
	i := 0
	for i < len(s) && (isDigit(s[i]) || s[i] == '.') {
		i++
	}
	if i == 0 || i == len(s) {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	unit := map[string]time.Duration{
		"ms": time.Millisecond, "s": time.Second, "m": time.Minute, "h": time.Hour,
		"d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
	}[s[i:]]
	if unit == 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return time.Duration(n * float64(unit)), nil
}

// IsMetric reports whether src looks like a metric query (starts with a function name).
func IsMetric(src string) bool {
	t := strings.TrimSpace(src)
	return t != "" && isIdentStart(t[0])
}

// ParseMetric parses a metric query:
//
//	count_over_time({service="api"} |= "error" [5m])
//	sum by (level) (rate({env="prod"} | json [1m]))
//	topk(10, sum by (route) (count_over_time({service="api"} | json | status>=500 [1h])))
func ParseMetric(src string) (MetricExpr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	e, err := p.metricExpr(0)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, p.errf(t, "unexpected %s after the metric expression", t.kind)
	}
	return e, nil
}

const maxNesting = 4

func (p *parser) metricExpr(depth int) (MetricExpr, error) {
	if depth > maxNesting {
		return nil, p.errf(p.peek(), "metric expression nested too deeply")
	}
	t := p.next()
	if t.kind != tIdent {
		return nil, p.errf(t, "expected a metric function (count_over_time, rate, bytes_over_time, sum, topk), found %s", t.kind)
	}
	switch t.text {
	case FuncCountOverTime, FuncRate, FuncBytesOverTime:
		return p.rangeAgg(t)
	case "sum":
		return p.sumAgg(depth)
	case "topk":
		return p.topK(depth)
	}
	return nil, p.errf(t, "unknown function %q (supported: count_over_time, rate, bytes_over_time, sum, topk)", t.text)
}

func (p *parser) rangeAgg(fn token) (MetricExpr, error) {
	if _, err := p.expect(tLParen); err != nil {
		return nil, err
	}
	q, err := p.logQuery()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tLBracket); err != nil {
		return nil, err
	}
	d, err := p.expect(tDuration)
	if err != nil {
		return nil, err
	}
	win, err := ParseDuration(d.text)
	if err != nil {
		return nil, p.errf(d, "%v", err)
	}
	if _, err := p.expect(tRBracket); err != nil {
		return nil, err
	}
	if _, err := p.expect(tRParen); err != nil {
		return nil, err
	}
	return &RangeAgg{Func: fn.text, Query: q, Window: win, WindowText: d.text}, nil
}

func (p *parser) byClause() ([]string, error) {
	if _, err := p.expect(tLParen); err != nil {
		return nil, err
	}
	var labels []string
	for p.peek().kind != tRParen {
		id, err := p.expect(tIdent)
		if err != nil {
			return nil, err
		}
		labels = append(labels, id.text)
		if p.peek().kind == tComma {
			p.next()
			continue
		}
		break
	}
	if _, err := p.expect(tRParen); err != nil {
		return nil, err
	}
	if len(labels) == 0 {
		return nil, p.errf(p.peek(), "by () needs at least one label")
	}
	return labels, nil
}

func (p *parser) sumAgg(depth int) (MetricExpr, error) {
	s := &SumAgg{}
	if t := p.peek(); t.kind == tIdent && t.text == "by" {
		p.next()
		by, err := p.byClause()
		if err != nil {
			return nil, err
		}
		s.By, s.HasBy = by, true
	}
	if _, err := p.expect(tLParen); err != nil {
		return nil, err
	}
	inner, err := p.metricExpr(depth + 1)
	if err != nil {
		return nil, err
	}
	if _, ok := inner.(*RangeAgg); !ok {
		return nil, p.errf(p.peek(), "sum needs a range aggregation such as rate(...) or count_over_time(...)")
	}
	s.Inner = inner
	if _, err := p.expect(tRParen); err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind == tIdent && t.text == "by" { // sum(...) by (labels)
		if s.HasBy {
			return nil, p.errf(t, "sum already has a by clause")
		}
		p.next()
		by, err := p.byClause()
		if err != nil {
			return nil, err
		}
		s.By, s.HasBy = by, true
	}
	return s, nil
}

func (p *parser) topK(depth int) (MetricExpr, error) {
	if _, err := p.expect(tLParen); err != nil {
		return nil, err
	}
	kTok, err := p.expect(tNumber)
	if err != nil {
		return nil, err
	}
	k, err := strconv.Atoi(kTok.text)
	if err != nil || k < 1 || k > 1000 {
		return nil, p.errf(kTok, "topk needs a whole number between 1 and 1000")
	}
	if _, err := p.expect(tComma); err != nil {
		return nil, err
	}
	inner, err := p.metricExpr(depth + 1)
	if err != nil {
		return nil, err
	}
	if _, ok := inner.(*TopK); ok {
		return nil, p.errf(p.peek(), "topk cannot wrap another topk")
	}
	if _, err := p.expect(tRParen); err != nil {
		return nil, err
	}
	return &TopK{K: k, Inner: inner}, nil
}
