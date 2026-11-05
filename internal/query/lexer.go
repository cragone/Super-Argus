// Package query implements ArgusQL: a LogQL-flavoured log query language with a
// hand-written lexer and parser, a streaming pipeline, and the query engine
// that plans over the stream index and chunk files.
package query

import (
	"fmt"
	"strconv"
	"strings"
)

type tokKind int

const (
	tEOF tokKind = iota
	tLBrace
	tRBrace
	tComma
	tIdent
	tString
	tNumber
	tEq     // =
	tNeq    // !=
	tRe     // =~
	tNre    // !~
	tPipe   // |
	tPipeEq // |=
	tPipeRe // |~
	tGt
	tGte
	tLt
	tLte
)

var tokNames = map[tokKind]string{
	tEOF: "end of query", tLBrace: "'{'", tRBrace: "'}'", tComma: "','", tIdent: "identifier",
	tString: "string", tNumber: "number", tEq: "'='", tNeq: "'!='", tRe: "'=~'", tNre: "'!~'",
	tPipe: "'|'", tPipeEq: "'|='", tPipeRe: "'|~'", tGt: "'>'", tGte: "'>='", tLt: "'<'", tLte: "'<='",
}

func (k tokKind) String() string { return tokNames[k] }

type token struct {
	kind tokKind
	text string // identifier name, unquoted string, or number literal
	pos  int    // byte offset in the query
}

// SyntaxError reports a lexing or parsing failure with its position.
type SyntaxError struct {
	Pos int
	Msg string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("syntax error at position %d: %s", e.Pos, e.Msg)
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func lex(src string) ([]token, error) {
	var toks []token
	i := 0
	emit := func(k tokKind, text string, pos, n int) {
		toks = append(toks, token{k, text, pos})
		i += n
	}
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '{':
			emit(tLBrace, "", i, 1)
		case c == '}':
			emit(tRBrace, "", i, 1)
		case c == ',':
			emit(tComma, "", i, 1)
		case c == '=':
			if strings.HasPrefix(src[i:], "=~") {
				emit(tRe, "", i, 2)
			} else {
				emit(tEq, "", i, 1)
			}
		case c == '!':
			switch {
			case strings.HasPrefix(src[i:], "!="):
				emit(tNeq, "", i, 2)
			case strings.HasPrefix(src[i:], "!~"):
				emit(tNre, "", i, 2)
			default:
				return nil, &SyntaxError{i, "unexpected '!'"}
			}
		case c == '|':
			switch {
			case strings.HasPrefix(src[i:], "|="):
				emit(tPipeEq, "", i, 2)
			case strings.HasPrefix(src[i:], "|~"):
				emit(tPipeRe, "", i, 2)
			default:
				emit(tPipe, "", i, 1)
			}
		case c == '>':
			if strings.HasPrefix(src[i:], ">=") {
				emit(tGte, "", i, 2)
			} else {
				emit(tGt, "", i, 1)
			}
		case c == '<':
			if strings.HasPrefix(src[i:], "<=") {
				emit(tLte, "", i, 2)
			} else {
				emit(tLt, "", i, 1)
			}
		case c == '"':
			j := i + 1
			for j < len(src) && src[j] != '"' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(src) {
				return nil, &SyntaxError{i, "unterminated string"}
			}
			s, err := strconv.Unquote(src[i : j+1])
			if err != nil {
				return nil, &SyntaxError{i, "invalid escape in string; write \\\\ for a backslash (e.g. \"\\\\d+\") or use a `raw` string"}
			}
			emit(tString, s, i, j+1-i)
		case c == '`':
			j := strings.IndexByte(src[i+1:], '`')
			if j < 0 {
				return nil, &SyntaxError{i, "unterminated raw string"}
			}
			emit(tString, src[i+1:i+1+j], i, j+2)
		case isDigit(c) || (c == '-' && i+1 < len(src) && isDigit(src[i+1])):
			j := i + 1
			for j < len(src) && (isDigit(src[j]) || src[j] == '.') {
				j++
			}
			if _, err := strconv.ParseFloat(src[i:j], 64); err != nil {
				return nil, &SyntaxError{i, fmt.Sprintf("invalid number %q", src[i:j])}
			}
			emit(tNumber, src[i:j], i, j-i)
		case isIdentStart(c):
			j := i + 1
			for j < len(src) && (isIdentStart(src[j]) || isDigit(src[j])) {
				j++
			}
			emit(tIdent, src[i:j], i, j-i)
		default:
			return nil, &SyntaxError{i, fmt.Sprintf("unexpected character %q", c)}
		}
	}
	toks = append(toks, token{tEOF, "", len(src)})
	return toks, nil
}
