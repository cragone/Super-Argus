package query

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const metricMsg = "metric queries are not supported yet; start with a stream selector like {service=\"api\"}"

// Parse parses a log query: a stream selector followed by pipeline stages.
//
//	{service="api", env=~"prod|staging"} |= "timeout" != "healthcheck" | json | level="error" | status>=500
func Parse(src string) (*Query, error) {
	toks, err := lex(src)
	if err != nil {
		if t := strings.TrimSpace(src); t != "" && isIdentStart(t[0]) {
			return nil, &SyntaxError{0, metricMsg}
		}
		return nil, err
	}
	p := &parser{toks: toks}
	return p.query()
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *parser) errf(t token, format string, a ...any) error {
	return &SyntaxError{t.pos, fmt.Sprintf(format, a...)}
}

func (p *parser) expect(k tokKind) (token, error) {
	t := p.next()
	if t.kind != k {
		return t, p.errf(t, "expected %s, found %s", k, t.kind)
	}
	return t, nil
}

func (p *parser) query() (*Query, error) {
	if t := p.peek(); t.kind == tIdent {
		return nil, p.errf(t, "%s", metricMsg)
	}
	q, err := p.logQuery()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, p.errf(t, "expected a pipeline stage (|= != |~ !~ | json | logfmt | field filter), found %s", t.kind)
	}
	return q, nil
}

func isStageStart(k tokKind) bool {
	return k == tPipeEq || k == tNeq || k == tPipeRe || k == tNre || k == tPipe
}

// logQuery parses a stream selector and its pipeline stages, stopping at the
// first token that cannot start a stage (end of input, ']' or ')').
func (p *parser) logQuery() (*Query, error) {
	if _, err := p.expect(tLBrace); err != nil {
		return nil, err
	}
	q := &Query{}
	for p.peek().kind != tRBrace {
		m, err := p.matcher()
		if err != nil {
			return nil, err
		}
		q.Matchers = append(q.Matchers, m)
		if p.peek().kind == tComma {
			p.next()
			continue
		}
		break
	}
	closeTok, err := p.expect(tRBrace)
	if err != nil {
		return nil, err
	}
	if len(q.Matchers) == 0 {
		return nil, p.errf(closeTok, "selector needs at least one matcher")
	}
	if !hasNonEmptyMatcher(q.Matchers) {
		return nil, p.errf(closeTok, "selector must contain at least one matcher that cannot match an empty value, e.g. service=\"api\"")
	}
	for isStageStart(p.peek().kind) {
		s, err := p.stage()
		if err != nil {
			return nil, err
		}
		q.Stages = append(q.Stages, s)
	}
	return q, nil
}

func (p *parser) matcher() (Matcher, error) {
	name, err := p.expect(tIdent)
	if err != nil {
		return Matcher{}, err
	}
	opTok := p.next()
	var op Op
	switch opTok.kind {
	case tEq:
		op = OpEq
	case tNeq:
		op = OpNeq
	case tRe:
		op = OpRe
	case tNre:
		op = OpNre
	default:
		return Matcher{}, p.errf(opTok, "expected one of = != =~ !~ after label %q, found %s", name.text, opTok.kind)
	}
	val, err := p.expect(tString)
	if err != nil {
		return Matcher{}, err
	}
	if op == OpRe || op == OpNre {
		if _, err := regexp.Compile(val.text); err != nil {
			return Matcher{}, p.errf(val, "invalid regular expression: %v", err)
		}
	}
	return Matcher{name.text, op, val.text}, nil
}

// hasNonEmptyMatcher reports whether some matcher cannot match a missing/empty label.
func hasNonEmptyMatcher(ms []Matcher) bool {
	for _, m := range ms {
		switch m.Op {
		case OpEq:
			if m.Value != "" {
				return true
			}
		case OpRe:
			if !anchored(m.Value).MatchString("") {
				return true
			}
		}
	}
	return false
}

func anchored(expr string) *regexp.Regexp {
	return regexp.MustCompile("^(?:" + expr + ")$")
}

func (p *parser) stage() (Stage, error) {
	t := p.next()
	switch t.kind {
	case tPipeEq, tNeq, tPipeRe, tNre:
		val, err := p.expect(tString)
		if err != nil {
			return nil, err
		}
		op := map[tokKind]Op{tPipeEq: OpEq, tNeq: OpNeq, tPipeRe: OpRe, tNre: OpNre}[t.kind]
		if op == OpRe || op == OpNre {
			if _, err := regexp.Compile(val.text); err != nil {
				return nil, p.errf(val, "invalid regular expression: %v", err)
			}
		}
		return LineFilter{op, val.text}, nil
	case tPipe:
		return p.pipeStage()
	}
	return nil, p.errf(t, "expected a pipeline stage (|= != |~ !~ | json | logfmt | field filter), found %s", t.kind)
}

func (p *parser) pipeStage() (Stage, error) {
	name, err := p.expect(tIdent)
	if err != nil {
		return nil, err
	}
	opTok := p.peek()
	var op Op
	switch opTok.kind {
	case tEq:
		op = OpEq
	case tNeq:
		op = OpNeq
	case tRe:
		op = OpRe
	case tNre:
		op = OpNre
	case tGt:
		op = OpGt
	case tGte:
		op = OpGte
	case tLt:
		op = OpLt
	case tLte:
		op = OpLte
	default:
		if name.text == "json" || name.text == "logfmt" {
			return Parser{name.text}, nil
		}
		return nil, p.errf(name, "unknown pipeline stage %q (supported: json, logfmt, or a field filter like level=\"error\")", name.text)
	}
	p.next()
	f := FieldFilter{Name: name.text, Op: op}
	val := p.next()
	switch {
	case (op == OpRe || op == OpNre) && val.kind != tString:
		return nil, p.errf(val, "regular expression operand must be a string, found %s", val.kind)
	case val.kind == tString:
		f.Value, f.Literal = val.text, strconv.Quote(val.text)
		if op == OpGt || op == OpGte || op == OpLt || op == OpLte {
			return nil, p.errf(val, "operator %s needs a number, found a string", op)
		}
		if op == OpRe || op == OpNre {
			if _, err := regexp.Compile(val.text); err != nil {
				return nil, p.errf(val, "invalid regular expression: %v", err)
			}
		}
	case val.kind == tNumber:
		n, _ := strconv.ParseFloat(val.text, 64)
		f.Value, f.Literal, f.Num, f.IsNum = val.text, val.text, n, true
	default:
		return nil, p.errf(val, "expected a string or number after %s, found %s", op, val.kind)
	}
	return f, nil
}
