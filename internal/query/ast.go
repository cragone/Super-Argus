package query

import (
	"strconv"
	"strings"
)

// Op is a comparison operator.
type Op int

// Operators. For line filters OpEq means "contains" and OpNeq "does not contain".
const (
	OpEq Op = iota
	OpNeq
	OpRe
	OpNre
	OpGt
	OpGte
	OpLt
	OpLte
)

var opText = [...]string{"=", "!=", "=~", "!~", ">", ">=", "<", "<="}

func (o Op) String() string { return opText[o] }

// Matcher is one label matcher of the stream selector.
type Matcher struct {
	Name  string
	Op    Op
	Value string
}

// Stage is one pipeline stage after the selector.
type Stage interface {
	// String renders the stage in canonical ArgusQL.
	String() string
}

// LineFilter is |= != |~ !~ against the whole line.
type LineFilter struct {
	Op    Op // OpEq (contains), OpNeq, OpRe, OpNre
	Value string
}

// Parser is `| json` or `| logfmt`.
type Parser struct{ Kind string }

// FieldFilter compares a parsed field (or stream label) after a parser stage.
type FieldFilter struct {
	Name    string
	Op      Op
	Value   string  // string operand (also the literal text of a numeric operand)
	Num     float64 // numeric operand when IsNum
	IsNum   bool
	Literal string // canonical text of the operand as written
}

// Query is a parsed log query.
type Query struct {
	Matchers []Matcher
	Stages   []Stage
}

func (s LineFilter) String() string {
	return [...]string{"|=", "!=", "|~", "!~"}[s.Op] + " " + strconv.Quote(s.Value)
}

func (s Parser) String() string { return "| " + s.Kind }

func (s FieldFilter) String() string {
	return "| " + s.Name + " " + s.Op.String() + " " + s.Literal
}

// String renders the query in canonical form.
func (q *Query) String() string {
	var b strings.Builder
	b.WriteByte('{')
	for i, m := range q.Matchers {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(m.Name + m.Op.String() + strconv.Quote(m.Value))
	}
	b.WriteByte('}')
	for _, s := range q.Stages {
		b.WriteByte(' ')
		b.WriteString(s.String())
	}
	return b.String()
}
