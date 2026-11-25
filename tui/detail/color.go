package detail

import (
	"encoding/json/jsontext"
	"errors"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/loderunner/logi/tui/termtext"
)

// Styles of the JSON tokens. Punctuation and whitespace keep the terminal's
// foreground.
var (
	nameStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.ANSIColor(ansi.Blue))
	stringStyle = lipgloss.NewStyle().
			Foreground(lipgloss.ANSIColor(ansi.Green))
	numberStyle = lipgloss.NewStyle().
			Foreground(lipgloss.ANSIColor(ansi.Yellow))
	boolStyle = lipgloss.NewStyle().
			Foreground(lipgloss.ANSIColor(ansi.Cyan))
	nullStyle = lipgloss.NewStyle().
			Foreground(lipgloss.ANSIColor(ansi.BrightBlack))
)

// colorize returns the indented JSON text with its names, strings, numbers,
// booleans and null in color. Every piece is sanitized before it is styled,
// and the escape sequences come only from the styles, so text from a log can't
// send its own to the terminal. It returns the sanitized text without color
// when it can't walk the JSON.
func colorize(indented []byte) string {
	colored, err := walk(indented)
	if err != nil {
		return sanitizeLines(string(indented))
	}

	return colored
}

// walk copies text to its result, with each token styled. A decoder tells a
// name from a string value, which the text alone can't. The decoder only
// reports where a token ends, so the gap between two tokens (whitespace, ":"
// and ",") is whatever lies between the end of the last token and the start of
// this one. Pieces are cut from the original bytes, so sanitizing, which can
// change their lengths, can't shift an offset.
func walk(text []byte) (string, error) {
	dec := jsontext.NewDecoder(
		strings.NewReader(string(text)),
		jsontext.AllowDuplicateNames(true),
		jsontext.AllowInvalidUTF8(true),
	)

	var (
		out strings.Builder
		pos int
	)

	for {
		kind := dec.PeekKind()
		structural := kind == jsontext.KindBeginObject ||
			kind == jsontext.KindBeginArray ||
			kind == jsontext.KindEndObject ||
			kind == jsontext.KindEndArray

		var (
			tokenLen int
			err      error
		)

		// Only a string can be a name, and the decoder must be asked before
		// it reads the token.
		style := valueStyle(kind)
		if kind == jsontext.KindString && isName(dec) {
			style = nameStyle
		}

		if structural {
			_, err = dec.ReadToken()
			tokenLen = 1
		} else {
			var v jsontext.Value

			v, err = dec.ReadValue()
			tokenLen = len(v)
		}

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return "", err
		}

		end := int(dec.InputOffset())
		start := end - tokenLen

		out.WriteString(sanitizeLines(string(text[pos:start])))

		token := termtext.Sanitize(string(text[start:end]))
		if structural {
			out.WriteString(token)
		} else {
			out.WriteString(style.Render(token))
		}

		pos = end
	}

	out.WriteString(sanitizeLines(string(text[pos:])))

	return out.String(), nil
}

// isName reports whether the next token is the name of an object member, which
// is the case when the innermost value is an object with an even number of
// tokens decoded.
func isName(dec *jsontext.Decoder) bool {
	kind, n := dec.StackIndex(dec.StackDepth())

	return kind == jsontext.KindBeginObject && n%2 == 0
}

// valueStyle returns the style of a value of the given kind, which is
// meaningful for scalars only.
func valueStyle(kind jsontext.Kind) lipgloss.Style {
	switch kind {
	case jsontext.KindString:
		return stringStyle
	case jsontext.KindNumber:
		return numberStyle
	case jsontext.KindTrue, jsontext.KindFalse:
		return boolStyle
	case jsontext.KindNull:
		return nullStyle
	default:
		return lipgloss.Style{}
	}
}

// sanitizeLines sanitizes s a line at a time, because termtext.Sanitize would
// replace the newlines.
func sanitizeLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = termtext.Sanitize(line)
	}

	return strings.Join(lines, "\n")
}
