// Package table is the table pane. It loads the rows on screen, newest first,
// and renders them as a table: a timestamp column, then one column per key.
package table

import (
	"context"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/loderunner/logi/ingest"
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

// Model is the table pane. It loads the page of rows it shows, newest first,
// from its source, and queries again when ingest reports new rows, when it
// scrolls, and when its height, its columns or its filter change. The top row
// is the selected row, in reverse video.
//
// A row's Values are its cells after the timestamp, one per column. When all
// of them are empty, the row shows its Raw line across those columns instead,
// which covers lines that aren't JSON and lines without the keys being shown.
type Model struct {
	source Rows
	keyMap keyMap

	rows          []logstore.Row
	width, height int

	// known lists every key seen so far, in order of appearance. timeKey is
	// the one the timestamp came from, or "" until ingest has found it.
	// columns are the keys after the timestamp, as defaultKeys picks them.
	known   []string
	timeKey string
	columns []string

	// filter is the filter of every query, or nil for every row.
	filter logstore.Filter

	// widths[i] is the widest cell shown so far in column i. It only grows
	// until the columns change, so a column doesn't jump when a short value
	// follows a long one.
	widths []int

	// message and level are the indexes of the message and level columns, or
	// noColumn.
	message, level int

	// from is the key of the top row on screen, or nil while the table
	// follows the newest row. It comes from the last page and is never
	// changed in place, so copies of the model share it.
	from *logstore.Key
	// move is the number of rows the keys have asked to scroll since the last
	// page arrived: positive is older, negative is newer. Each query repeats
	// it, so a query that a newer one replaces loses no key press.
	move int

	// gen numbers page queries. cancel stops the one in flight. A page with
	// an older gen is dropped.
	gen    int
	cancel context.CancelFunc
}

// noColumn is the index of a column the table doesn't have.
const noColumn = -1

// keyMap is the keys the table handles.
type keyMap struct {
	Up, Down, PageUp, PageDown key.Binding
	// Open asks to open the selected row in the detail pane.
	Open key.Binding
}

// OpenMsg asks the parent to show Row in the detail pane.
type OpenMsg struct {
	Row logstore.Row
}

// New returns an empty table that loads its pages from source. It shows the
// raw line as its only column until ingest reports keys.
func New(source Rows) Model {
	columns := defaultKeys(nil, "")

	return Model{
		source: source,
		keyMap: keyMap{
			Up:       key.NewBinding(key.WithKeys("up")),
			Down:     key.NewBinding(key.WithKeys("down")),
			PageUp:   key.NewBinding(key.WithKeys("pgup")),
			PageDown: key.NewBinding(key.WithKeys("pgdown")),
			Open:     key.NewBinding(key.WithKeys("enter")),
		},
		columns: columns,
		message: messageIndex(columns),
		level:   levelIndex(columns),
	}
}

// SetSize returns the table resized to width columns and height lines. A page
// holds as many rows as the table shows, so a new height loads the page again,
// from the same top row.
func (m Model) SetSize(width, height int) (Model, tea.Cmd) {
	resized := height != m.height
	m.width = width
	m.height = height

	if !resized {
		return m, nil
	}

	return m.refresh()
}

// SetFilter returns the table filtered by f, or showing every row when f is
// nil, and the query that loads the page again from the same top row.
func (m Model) SetFilter(f logstore.Filter) (Model, tea.Cmd) {
	m.filter = f

	return m.refresh()
}

// Following reports whether the table follows the newest row, which it does
// until it scrolls away from it.
func (m Model) Following() bool {
	return m.from == nil
}

// Update handles the keys that scroll the table and open the selected row,
// the progress reports of ingest, which bring new rows and keys, and the
// pages of its own queries.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.key(msg)

	case ingest.Ingested:
		m = m.setKnown(msg.NewKeys, msg.TimeKey)

		return m.refresh()

	case PageMsg:
		if msg.gen != m.gen {
			return m, nil
		}

		m.from = msg.page.From
		m.move = 0
		m = m.setRows(msg.page.Rows)
	}

	return m, nil
}

// key handles a key press. Enter on an empty table opens nothing.
func (m Model) key(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keyMap.Up):
		return m.scroll(-1)
	case key.Matches(msg, m.keyMap.Down):
		return m.scroll(1)
	case key.Matches(msg, m.keyMap.PageUp):
		return m.scroll(-m.height)
	case key.Matches(msg, m.keyMap.PageDown):
		return m.scroll(m.height)
	case key.Matches(msg, m.keyMap.Open):
		if len(m.rows) == 0 {
			return m, nil
		}

		row := m.rows[0]

		return m, func() tea.Msg { return OpenMsg{Row: row} }
	}

	return m, nil
}

// scroll asks to move the view by n rows, positive for older rows, and loads
// the page again.
func (m Model) scroll(n int) (Model, tea.Cmd) {
	m.move += n

	return m.refresh()
}

// setKnown records newly seen keys and the time key and, when they change the
// default columns, switches to them and forgets the column widths. The query
// that follows loads the rows for the new columns, and its generation number
// drops the pages of the old ones.
func (m Model) setKnown(newKeys []string, timeKey string) Model {
	m.known = append(m.known, newKeys...)
	m.timeKey = timeKey

	columns := defaultKeys(m.known, m.timeKey)
	if slices.Equal(columns, m.columns) {
		return m
	}

	m.columns = columns
	m.message = messageIndex(columns)
	m.level = levelIndex(columns)
	m.widths = nil
	m.widths = m.grownWidths(m.rows)

	return m
}

// setRows returns the table showing rows, which it keeps without copying.
// Every row must have one Value per column.
func (m Model) setRows(rows []logstore.Row) Model {
	m.rows = rows
	m.widths = m.grownWidths(rows)

	return m
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
