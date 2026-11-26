// Package kql parses the filter language into a [logstore.Filter].
//
// The parser builds filters directly, with no AST of its own: a filter is
// already an immutable tree of values, and only logstore knows how to run it.
//
// The grammar so far is one term, "field:value":
//
//	filter = ws [ term ws ] EOF
//	term   = field ":" value
//	field  = quoted | 1*( char except whitespace, ":" and a leading '"' )
//	value  = quoted | 1*( char except whitespace )
//	quoted = '"' *( char except '"' and '\' | '\"' | '\\' ) '"'
//
// A field is the exact, case-sensitive flattened key, such as "http.method".
// A bare value runs to the next whitespace and reserves no characters, so
// "time:12:30" is the field "time" and the value "12:30". A double quote is
// special only at the start of a field or value. Inside quotes, only \" and
// \\ are escapes. Any other backslash sequence is an error, which keeps it
// free for later syntax.
package kql

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/loderunner/logi/logstore"
)

// Error is a syntax error in a filter. Callers can get it from the error that
// [Parse] returns with [errors.As].
type Error struct {
	// Col is the 1-based column of the problem, counted in runes. It is one
	// past the last rune when the filter ends too soon.
	Col int
	// Msg says what is wrong, without the column.
	Msg string
}

func (e *Error) Error() string {
	return fmt.Sprintf("col %d: %s", e.Col, e.Msg)
}

// Parse parses s into a filter. Empty or whitespace-only input returns a nil
// filter, which keeps every row. A syntax error is an [*Error].
func Parse(s string) (logstore.Filter, error) {
	p := parser{in: []rune(s)}

	return p.filter()
}

// parser is a recursive-descent parser with one method per grammar rule. Each
// method starts at pos, and on success leaves pos just after what it read.
type parser struct {
	in  []rune
	pos int
}

// fail returns an Error at the rune index pos.
func (p *parser) fail(pos int, format string, args ...any) error {
	return &Error{Col: pos + 1, Msg: fmt.Sprintf(format, args...)}
}

func (p *parser) eof() bool {
	return p.pos >= len(p.in)
}

// at reports whether the rune at pos is r.
func (p *parser) at(r rune) bool {
	return !p.eof() && p.in[p.pos] == r
}

func (p *parser) skipSpace() {
	for !p.eof() && unicode.IsSpace(p.in[p.pos]) {
		p.pos++
	}
}

// filter = ws [ term ws ] EOF
func (p *parser) filter() (logstore.Filter, error) {
	p.skipSpace()

	if p.eof() {
		return nil, nil
	}

	f, err := p.term()
	if err != nil {
		return nil, err
	}

	p.skipSpace()

	if !p.eof() {
		return nil, p.fail(p.pos, "expected end of filter")
	}

	return f, nil
}

// term = field ":" value
func (p *parser) term() (logstore.Filter, error) {
	key, err := p.field()
	if err != nil {
		return nil, err
	}

	if !p.at(':') {
		return nil, p.fail(p.pos, "expected ':' after field")
	}

	p.pos++

	value, err := p.value()
	if err != nil {
		return nil, err
	}

	return logstore.Match{Key: key, Value: value}, nil
}

// field = quoted | 1*( char except whitespace, ":" and a leading '"' )
func (p *parser) field() (string, error) {
	if p.at('"') {
		return p.quoted()
	}

	start := p.pos
	for !p.eof() && p.in[p.pos] != ':' && !unicode.IsSpace(p.in[p.pos]) {
		p.pos++
	}

	if p.pos == start {
		return "", p.fail(start, "expected a field")
	}

	return string(p.in[start:p.pos]), nil
}

// value = quoted | 1*( char except whitespace )
func (p *parser) value() (string, error) {
	if p.at('"') {
		return p.quoted()
	}

	start := p.pos
	for !p.eof() && !unicode.IsSpace(p.in[p.pos]) {
		p.pos++
	}

	if p.pos == start {
		return "", p.fail(start, "expected a value")
	}

	return string(p.in[start:p.pos]), nil
}

// quoted = '"' *( char except '"' and '\' | '\"' | '\\' ) '"'
func (p *parser) quoted() (string, error) {
	open := p.pos
	p.pos++ // the opening quote

	var sb strings.Builder

	for !p.eof() {
		r := p.in[p.pos]

		switch r {
		case '"':
			p.pos++

			return sb.String(), nil
		case '\\':
			// A backslash at the very end falls out of the loop as an
			// unterminated quote.
			if p.pos+1 >= len(p.in) {
				p.pos++

				continue
			}

			next := p.in[p.pos+1]
			if next != '"' && next != '\\' {
				return "", p.fail(p.pos, `unknown escape \%c`, next)
			}

			sb.WriteRune(next)

			p.pos += 2
		default:
			sb.WriteRune(r)

			p.pos++
		}
	}

	return "", p.fail(open, "unterminated quote")
}
