package query

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Pipeline is a compiled query: label matchers and line/field stages.
type Pipeline struct {
	Matchers []compiledMatcher
	stages   []compiledStage
	// HasParser reports whether any stage extracts fields (json/logfmt).
	HasParser bool
}

type compiledMatcher struct {
	Matcher
	re *regexp.Regexp
}

type compiledStage struct {
	kind   int // stageLine, stageJSON, stageLogfmt, stageField
	line   LineFilter
	lineRe *regexp.Regexp
	field  FieldFilter
	fldRe  *regexp.Regexp
}

const (
	stageLine = iota
	stageJSON
	stageLogfmt
	stageField
)

// Compile precompiles regexes. The query must come from Parse.
func Compile(q *Query) (*Pipeline, error) {
	p := &Pipeline{}
	for _, m := range q.Matchers {
		cm := compiledMatcher{Matcher: m}
		if m.Op == OpRe || m.Op == OpNre {
			re, err := regexp.Compile("^(?:" + m.Value + ")$")
			if err != nil {
				return nil, err
			}
			cm.re = re
		}
		p.Matchers = append(p.Matchers, cm)
	}
	for _, s := range q.Stages {
		switch s := s.(type) {
		case LineFilter:
			cs := compiledStage{kind: stageLine, line: s}
			if s.Op == OpRe || s.Op == OpNre {
				re, err := regexp.Compile(s.Value)
				if err != nil {
					return nil, err
				}
				cs.lineRe = re
			}
			p.stages = append(p.stages, cs)
		case Parser:
			p.HasParser = true
			k := stageJSON
			if s.Kind == "logfmt" {
				k = stageLogfmt
			}
			p.stages = append(p.stages, compiledStage{kind: k})
		case FieldFilter:
			cs := compiledStage{kind: stageField, field: s}
			if s.Op == OpRe || s.Op == OpNre {
				re, err := regexp.Compile("^(?:" + s.Value + ")$")
				if err != nil {
					return nil, err
				}
				cs.fldRe = re
			}
			p.stages = append(p.stages, cs)
		}
	}
	return p, nil
}

// MatchLabels reports whether a stream's labels satisfy every matcher.
// A missing label behaves as the empty string.
func (p *Pipeline) MatchLabels(labels map[string]string) bool {
	for _, m := range p.Matchers {
		v := labels[m.Name]
		var ok bool
		switch m.Op {
		case OpEq:
			ok = v == m.Value
		case OpNeq:
			ok = v != m.Value
		case OpRe:
			ok = m.re.MatchString(v)
		case OpNre:
			ok = !m.re.MatchString(v)
		}
		if !ok {
			return false
		}
	}
	return true
}

// Process runs the pipeline over one line. It returns the extracted fields
// (nil when the query has no parser stage) and whether the line is kept.
func (p *Pipeline) Process(labels map[string]string, line string) (map[string]string, bool) {
	var fields map[string]string
	for i := range p.stages {
		s := &p.stages[i]
		switch s.kind {
		case stageLine:
			var hit bool
			if s.lineRe != nil {
				hit = s.lineRe.MatchString(line)
			} else {
				hit = strings.Contains(line, s.line.Value)
			}
			if s.line.Op == OpNeq || s.line.Op == OpNre {
				hit = !hit
			}
			if !hit {
				return nil, false
			}
		case stageJSON:
			if fields == nil {
				fields = map[string]string{}
			}
			parseJSON(line, fields)
		case stageLogfmt:
			if fields == nil {
				fields = map[string]string{}
			}
			parseLogfmt(line, fields)
		case stageField:
			v, found := fields[s.field.Name]
			if !found {
				v, found = labels[s.field.Name]
			}
			if !s.field.match(s.fldRe, v, found) {
				return nil, false
			}
		}
	}
	return fields, true
}

func (f *FieldFilter) match(re *regexp.Regexp, v string, found bool) bool {
	switch f.Op {
	case OpEq, OpNeq:
		var eq bool
		if f.IsNum {
			n, err := strconv.ParseFloat(v, 64)
			eq = found && err == nil && n == f.Num
		} else {
			eq = v == f.Value
		}
		return eq == (f.Op == OpEq)
	case OpRe:
		return re.MatchString(v)
	case OpNre:
		return !re.MatchString(v)
	}
	n, err := strconv.ParseFloat(v, 64)
	if !found || err != nil {
		return false
	}
	switch f.Op {
	case OpGt:
		return n > f.Num
	case OpGte:
		return n >= f.Num
	case OpLt:
		return n < f.Num
	case OpLte:
		return n <= f.Num
	}
	return false
}

// parseJSON flattens a JSON object into dst, joining nested keys with "_".
// Non-objects and invalid JSON leave dst unchanged.
func parseJSON(line string, dst map[string]string) {
	line = strings.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return
	}
	flatten("", v, dst)
}

func flatten(prefix string, v map[string]any, dst map[string]string) {
	for k, val := range v {
		key := sanitizeKey(k)
		if prefix != "" {
			key = prefix + "_" + key
		}
		switch x := val.(type) {
		case map[string]any:
			flatten(key, x, dst)
		case string:
			dst[key] = x
		case json.Number:
			dst[key] = x.String()
		case bool:
			dst[key] = strconv.FormatBool(x)
		case nil:
			dst[key] = ""
		default: // arrays
			b, _ := json.Marshal(x)
			dst[key] = string(b)
		}
	}
}

func sanitizeKey(k string) string {
	ok := true
	for i := 0; i < len(k); i++ {
		if !(isIdentStart(k[i]) || isDigit(k[i])) {
			ok = false
			break
		}
	}
	if ok {
		return k
	}
	return strings.Map(func(r rune) rune {
		if r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '_'
	}, k)
}

// parseLogfmt extracts key=value pairs; values may be double-quoted.
func parseLogfmt(line string, dst map[string]string) {
	i, n := 0, len(line)
	for i < n {
		for i < n && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		start := i
		for i < n && line[i] != '=' && line[i] != ' ' && line[i] != '\t' {
			i++
		}
		key := line[start:i]
		if key == "" {
			if i < n && line[i] == '=' { // stray '=': skip it
				i++
			}
			continue
		}
		if i >= n || line[i] != '=' {
			dst[sanitizeKey(key)] = ""
			continue
		}
		i++ // '='
		var val string
		if i < n && line[i] == '"' {
			j := i + 1
			for j < n && line[j] != '"' {
				if line[j] == '\\' {
					j++
				}
				j++
			}
			if j >= n {
				val = line[i+1:]
				i = n
			} else {
				if s, err := strconv.Unquote(line[i : j+1]); err == nil {
					val = s
				} else {
					val = line[i+1 : j]
				}
				i = j + 1
			}
		} else {
			vs := i
			for i < n && line[i] != ' ' && line[i] != '\t' {
				i++
			}
			val = line[vs:i]
		}
		dst[sanitizeKey(key)] = val
	}
}
