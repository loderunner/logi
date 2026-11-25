// Package table renders log rows as a table: a timestamp column, then one
// column per key.
package table

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/loderunner/logi/logstore"
	"github.com/loderunner/logi/tui/termtext"
)

const (
	// ellipsis ends a value that was cut short.
	ellipsis = "…"
	// separator goes between columns.
	separator = " "
	// maxColumnShare caps a column that isn't the last at 1/maxColumnShare of
	// the pane, so one long value can't push the others off screen.
	maxColumnShare = 4
	// minMessageCap is the least the message column's cap can be. The message
	// is the column you read, so a narrow pane doesn't squeeze it down to
	// 1/maxColumnShare of the pane like the others.
	minMessageCap = 40
)

// selectedStyle draws the selected row.
var selectedStyle = lipgloss.NewStyle().Reverse(true)

// Model is the table pane. It holds the rows to show, newest first, and the
// size of the pane. The top row is the selected row, in reverse video.
//
// A row's Values are its cells after the timestamp. When all of them are
// empty, the row shows its Raw line across those columns instead, which
// covers lines that aren't JSON and lines without the keys being shown.
type Model struct {
	rows          []logstore.Row
	width, height int

	// widths[i] is the widest cell shown so far in column i. It only grows
	// until ResetWidths, so a column doesn't jump when a short value follows a
	// long one.
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
// which can take more than 1/maxColumnShare of the pane, down to
// minMessageCap cells. Pass a negative i for none.
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
			widths[i] = max(
				widths[i],
				ansi.StringWidth(termtext.Sanitize(value)),
			)
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

// Selected returns the selected row, which is the top one, and false when the
// table is empty.
func (m Model) Selected() (logstore.Row, bool) {
	if len(m.rows) == 0 {
		return logstore.Row{}, false
	}

	return m.rows[0], true
}

// View renders one line per row, cut to the width, then blank lines up to the
// height.
func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	lines := make([]string, m.height)

	for i, row := range m.rows[:min(len(m.rows), m.height)] {
		lines[i] = m.line(row, i == 0)
	}

	return strings.Join(lines, "\n")
}

// timeLayout is the layout of FormatTime.
const timeLayout = "2006-01-02 15:04:05.000"

// FormatTime formats t as the timestamp column shows it, in UTC to the
// millisecond.
func FormatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// line renders one row, cut to the width. A selected row is in reverse video
// and padded to the width, so the highlight spans the pane.
func (m Model) line(row logstore.Row, selected bool) string {
	c := clip{left: m.width}

	c.value(FormatTime(row.TS))
	c.gap(separator)

	var level span
	if showsRaw(row) {
		c.value(termtext.Sanitize(row.Raw))
	} else {
		level = m.writeCells(&c, row.Values, selected)
	}

	if selected {
		c.gap(strings.Repeat(" ", c.left))

		return level.render(c.b.String(), selectedStyle)
	}

	// A row that ends in empty cells would otherwise end in spaces.
	return level.render(strings.TrimRight(c.b.String(), " "), lipgloss.Style{})
}

// writeCells writes the cells of a row. Each is padded to its column's width
// except the last, which is padded to the end of the line. It returns the span
// of the level cell, which is empty when the row has no known level.
func (m Model) writeCells(c *clip, values []string, selected bool) span {
	var level span

	for i, value := range values {
		value = termtext.Sanitize(value)
		cell, padding := value, ""

		if i < len(values)-1 {
			limit := m.width / maxColumnShare
			if i == m.message {
				limit = max(limit, minMessageCap)
			}

			// The widths cover the rows being shown.
			width := min(m.widths[i], limit)
			cell = ansi.Truncate(value, width, ellipsis)
			padding = strings.Repeat(" ", width-ansi.StringWidth(cell))
		}

		start := c.b.Len()
		c.value(cell)
		textEnd := c.b.Len()

		if i == len(values)-1 {
			padding = strings.Repeat(" ", c.left)
		}

		c.gap(padding)
		cellEnd := c.b.Len()

		if i < len(values)-1 {
			c.gap(separator)
		}

		if i != m.level {
			continue
		}

		// The style is chosen from the whole value, because a cut one such as
		// "wa…" no longer names its level.
		style, ok := levelStyle(value)
		if !ok {
			continue
		}

		// The color covers the text. On the selected row it is the background
		// of the whole cell, padding included.
		level = span{start: start, end: textEnd, style: style}
		if selected {
			level.end = cellEnd
			level.style = style.Reverse(true)
		}
	}

	return level
}

// span is the part of a line, in bytes from start to end, that has its own
// style.
type span struct {
	start, end int
	style      lipgloss.Style
}

// render returns line styled with base, except the span, which has its own
// style. The span is cut to the line, which may have been trimmed.
func (s span) render(line string, base lipgloss.Style) string {
	end := min(s.end, len(line))
	start := min(s.start, end)

	return renderRun(base, line[:start]) +
		renderRun(s.style, line[start:end]) +
		renderRun(base, line[end:])
}

// renderRun returns text in style, or nothing when text is empty, so that an
// empty run adds no escape sequences.
func renderRun(style lipgloss.Style, text string) string {
	if text == "" {
		return ""
	}

	return style.Render(text)
}

// clip writes a line of at most left more cells. Only a value cut short gets
// an ellipsis, so one shows that something is hidden. The line can end in a
// gap, or at the edge of a value that fits, and shows none.
type clip struct {
	b strings.Builder
	// left is the number of cells the line still has room for.
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

// write writes s, or the start of it ending in tail if it doesn't fit.
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
