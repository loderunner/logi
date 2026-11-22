// Package table renders log rows as a one-column table.
package table

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/loderunner/logi/logstore"
)

const (
	ellipsis = "…"
	// tabWidth is how many spaces replace a tab.
	tabWidth = 4
)

// Model is the table pane. It holds the rows to show, newest first, and the
// size of the pane.
type Model struct {
	rows          []logstore.Row
	width, height int
}

// New returns an empty table.
func New() Model {
	return Model{}
}

// SetSize returns the table resized to width columns and height lines.
func (m Model) SetSize(width, height int) Model {
	m.width = width
	m.height = height

	return m
}

// SetRows returns the table showing rows, which it keeps without copying.
// The caller must not change the slice afterwards.
func (m Model) SetRows(rows []logstore.Row) Model {
	m.rows = rows

	return m
}

// Rows returns the rows the table shows.
func (m Model) Rows() []logstore.Row {
	return m.rows
}

// View renders one line per row, cut to the width, then blank lines up to the
// height.
func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	lines := make([]string, m.height)

	for i := range lines {
		if i < len(m.rows) {
			lines[i] = ansi.Truncate(sanitize(m.rows[i].Raw), m.width, ellipsis)
		}
	}

	return strings.Join(lines, "\n")
}

// sanitize makes s safe to print. Control characters, ESC included, become
// U+FFFD so that a log line can't send escape sequences to the terminal, and
// tabs become spaces. Invalid UTF-8 also becomes U+FFFD.
func sanitize(s string) string {
	s = strings.ReplaceAll(s, "\t", strings.Repeat(" ", tabWidth))

	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return unicode.ReplacementChar
		}

		return r
	}, s)
}
