// Package table renders log rows as a table: a timestamp column, then one
// column per key.
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
	// timeLayout formats the timestamp column, in UTC.
	timeLayout = "2006-01-02 15:04:05.000"
	// separator goes between columns.
	separator = " "
	// maxColumnShare caps a column that isn't the last at this fraction of
	// the pane, so one long value can't push the others off screen.
	maxColumnShare = 4
	// minMessageCap is the least the message column's cap can be. The message
	// is the column you read, so a narrow pane doesn't squeeze it down to a
	// quarter of the pane like the others.
	minMessageCap = 40
)

// Model is the table pane. It holds the rows to show, newest first, and the
// size of the pane.
//
// A row's Values are its cells after the timestamp. When all of them are
// empty, the row shows its Raw line across those columns instead, which
// covers lines that aren't JSON and lines without the keys being shown.
type Model struct {
	rows          []logstore.Row
	width, height int

	// widths[i] is the widest cell shown so far in column i. It only grows,
	// so a column doesn't jump when a short value follows a long one.
	widths []int

	// message and level are the indexes of the message and level columns, or
	// noColumn.
	message, level int
}

// noColumn is the index of a column the table doesn't have.
const noColumn = -1

// New returns an empty table.
func New() Model {
	return Model{message: noColumn, level: noColumn}
}

// SetMessageColumn returns the table treating column i as the message column,
// which has a floor on its cap. Pass a negative i for none. Call
// ResetWidths after it, because the widths depend on the caps.
func (m Model) SetMessageColumn(i int) Model {
	if i < 0 {
		i = noColumn
	}

	m.message = i

	return m
}

// SetLevelColumn returns the table coloring the values of column i by log
// level. Pass a negative i for none.
func (m Model) SetLevelColumn(i int) Model {
	if i < 0 {
		i = noColumn
	}

	m.level = i

	return m
}

// SetSize returns the table resized to width columns and height lines.
func (m Model) SetSize(width, height int) Model {
	m.width = width
	m.height = height

	return m
}

// SetRows returns the table showing rows, which it keeps without copying.
// The caller must not change the slice afterwards. Every row must have the
// same number of Values.
func (m Model) SetRows(rows []logstore.Row) Model {
	m.rows = rows
	m.widths = m.grownWidths(rows)

	return m
}

// ResetWidths returns the table with its column widths forgotten, sized again
// to the rows it holds. Call it when the columns change.
func (m Model) ResetWidths() Model {
	m.widths = nil
	m.widths = m.grownWidths(m.rows)

	return m
}

// Rows returns the rows the table shows.
func (m Model) Rows() []logstore.Row {
	return m.rows
}

// grownWidths returns the column widths widened to fit rows. It returns a
// new slice, because a copy of the model may still use the old one.
func (m Model) grownWidths(rows []logstore.Row) []int {
	var widths []int

	for _, row := range rows {
		if showsRaw(row) {
			continue
		}

		if widths == nil {
			widths = make([]int, len(row.Values))
			copy(widths, m.widths)
		}

		for i, value := range row.Values {
			widths[i] = max(widths[i], ansi.StringWidth(sanitize(value)))
		}
	}

	if widths == nil {
		return m.widths
	}

	return widths
}

// showsRaw reports whether the row has no cell to show.
func showsRaw(row logstore.Row) bool {
	for _, value := range row.Values {
		if value != "" {
			return false
		}
	}

	return true
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
			lines[i] = m.line(m.rows[i])
		}
	}

	return strings.Join(lines, "\n")
}

// line renders one row, cut to the width.
func (m Model) line(row logstore.Row) string {
	c := clip{left: m.width}

	c.value(row.TS.UTC().Format(timeLayout))
	c.gap(separator)

	if showsRaw(row) {
		c.value(sanitize(row.Raw))
	} else {
		m.writeCells(&c, row.Values)
	}

	// A row that ends in empty cells would otherwise end in spaces.
	return strings.TrimRight(c.b.String(), " ")
}

// writeCells writes the cells of a row. Each is padded to its column's width
// except the last, which takes whatever the line has left.
func (m Model) writeCells(c *clip, values []string) {
	for i, value := range values {
		value = sanitize(value)

		if i == len(values)-1 {
			c.value(m.renderCell(i, value, value))
			return
		}

		limit := m.width / maxColumnShare
		if i == m.message {
			limit = max(limit, minMessageCap)
		}

		// The widths cover the rows being shown.
		width := min(m.widths[i], limit)
		cell := ansi.Truncate(value, width, ellipsis)
		padding := strings.Repeat(" ", width-ansi.StringWidth(cell))

		// The color is chosen from the whole value, because a cut one such as
		// "wa…" no longer names its level. It covers the text, not the padding.
		c.value(m.renderCell(i, value, cell))
		c.gap(padding + separator)
	}
}

// renderCell returns text colored by the log level that value names, when column
// i is the level column and the level is known. Otherwise it returns text.
func (m Model) renderCell(i int, value, text string) string {
	if i != m.level || text == "" {
		return text
	}

	style, ok := levelStyle(value)
	if !ok {
		return text
	}

	return style.Render(text)
}

// clip writes a line of at most left more cells. Only a value cut short gets
// an ellipsis, so one shows that something is hidden. The line can end in a
// gap, or at the edge of a value that fits, and shows none.
type clip struct {
	b    strings.Builder
	left int
}

// value writes s, or the start of it with an ellipsis if it doesn't fit.
func (c *clip) value(s string) {
	c.write(s, ellipsis)
}

// gap writes padding or a separator, or the start of it if it doesn't fit.
func (c *clip) gap(s string) {
	c.write(s, "")
}

func (c *clip) write(s, tail string) {
	if c.left <= 0 {
		return
	}

	width := ansi.StringWidth(s)
	if width > c.left {
		s = ansi.Truncate(s, c.left, tail)
		width = c.left
	}

	c.b.WriteString(s)
	c.left -= width
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
