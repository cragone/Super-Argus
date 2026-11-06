package ingest

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
)

// Built-in redaction rules, selectable by name in ARGUS_REDACT.
var builtinRedactions = map[string]*regexp.Regexp{
	"email":  regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
	"bearer": regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/\-]+=*`),
	"card":   regexp.MustCompile(`\b\d(?:[ \-]?\d){12,18}\b`),
	"ip":     regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`),
}

// RedactionNames lists the built-in rule names.
func RedactionNames() []string { return []string{"email", "bearer", "card", "ip"} }

// Policy applies redaction and sampling to lines before they reach the WAL.
// The zero value (or nil) does nothing.
type Policy struct {
	redact []namedRule
	// sample maps a service label value to the percentage of non-warn lines kept.
	// "*" is the fallback. Services without a rule keep everything.
	sample map[string]float64
	rnd    func() float64 // [0,1); injectable for tests
}

type namedRule struct {
	name string
	re   *regexp.Regexp
}

// ParsePolicy builds a Policy from the ARGUS_REDACT and ARGUS_SAMPLE settings.
//
// redact is a comma list of built-in names (email, bearer, card, ip) or
// "all". sample is a comma list of service=percent pairs, e.g.
// "api=25,worker=10,*=50" (percent of lines kept; warn and above are always kept).
func ParsePolicy(redact, sample string) (*Policy, error) {
	p := &Policy{rnd: rand.Float64}
	for _, n := range splitList(redact) {
		if n == "all" {
			p.redact = nil
			for _, name := range RedactionNames() {
				p.redact = append(p.redact, namedRule{name, builtinRedactions[name]})
			}
			break
		}
		re, ok := builtinRedactions[n]
		if !ok {
			return nil, fmt.Errorf("unknown redaction rule %q (valid: %s, all)", n, strings.Join(RedactionNames(), ", "))
		}
		p.redact = append(p.redact, namedRule{n, re})
	}
	for _, kv := range splitList(sample) {
		svc, pct, ok := strings.Cut(kv, "=")
		f, err := strconv.ParseFloat(strings.TrimSpace(pct), 64)
		if !ok || strings.TrimSpace(svc) == "" || err != nil || f < 0 || f > 100 {
			return nil, fmt.Errorf("bad sampling rule %q (want service=percent, 0-100)", kv)
		}
		if p.sample == nil {
			p.sample = map[string]float64{}
		}
		p.sample[strings.TrimSpace(svc)] = f
	}
	if len(p.redact) == 0 && len(p.sample) == 0 {
		return nil, nil
	}
	return p, nil
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Redact replaces matches of the configured rules with [REDACTED:<rule>].
func (p *Policy) Redact(line string) string {
	if p == nil {
		return line
	}
	for _, r := range p.redact {
		line = r.re.ReplaceAllString(line, "[REDACTED:"+r.name+"]")
	}
	return line
}

var levelRE = regexp.MustCompile(`(?i)(?:"(?:level|lvl|severity)"\s*:\s*"|\b(?:level|lvl|severity)=)([a-z]+)`)

// isImportant reports whether a line is at warn level or above. The label
// "level" wins; otherwise the line is searched for a JSON or logfmt level.
func isImportant(labels map[string]string, line string) bool {
	lv := labels["level"]
	if lv == "" {
		if m := levelRE.FindStringSubmatch(line); m != nil {
			lv = m[1]
		}
	}
	switch strings.ToLower(lv) {
	case "warn", "warning", "error", "err", "fatal", "panic", "critical", "crit", "alert", "emerg":
		return true
	}
	return false
}

// Keep reports whether the line survives sampling.
func (p *Policy) Keep(labels map[string]string, line string) bool {
	if p == nil || len(p.sample) == 0 {
		return true
	}
	pct, ok := p.sample[labels["service"]]
	if !ok {
		if pct, ok = p.sample["*"]; !ok {
			return true
		}
	}
	if pct >= 100 || isImportant(labels, line) {
		return true
	}
	return p.rnd()*100 < pct
}
