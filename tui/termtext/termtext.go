// Package termtext makes text from a log safe to print in a terminal pane.
package termtext

import (
	"strings"
	"unicode"
)

// tabWidth is how many spaces replace a tab.
const tabWidth = 4

// Sanitize makes s safe to print. Control characters, ESC included, become
// U+FFFD so that a log line can't send escape sequences to the terminal, and
// tabs become spaces. Invalid UTF-8 also becomes U+FFFD.
func Sanitize(s string) string {
	s = strings.ReplaceAll(s, "\t", strings.Repeat(" ", tabWidth))

	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return unicode.ReplacementChar
		}

		return r
	}, s)
}
