// Package detail renders one row's JSON, indented, in a framed pane the size
// of the table's.
package detail

import (
	"encoding/json/jsontext"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// indent is what each level of nesting adds to a line.
const indent = "  "

// Model is the detail pane. It holds the lines of one row, the title of its
// frame and the size of the pane. A setter returns a changed copy, and Update
// handles the keys that scroll it.
type Model struct {
	title string
	// text is the row's lines before wrapping. lines is text wrapped to the
	// width inside the frame, so the pane shows a value whole instead of
	// cutting it with "…".
	text, lines   []string
	width, height int
	// offset is the index in lines of the first line shown. It stays within
	// [0, maxOffset].
	offset int
}

// New returns an empty pane.
func New() Model {
	return Model{}
}

// SetSize returns the pane resized to width columns and height lines, with
// its lines wrapped again.
func (m Model) SetSize(width, height int) Model {
	m.width = width
	m.height = height
	m.lines = wrap(m.text, m.textWidth())
	m.offset = min(m.offset, m.maxOffset())

	return m
}

// Update handles the keys that scroll the pane: the arrows by a line, and the
// page keys by the height of the text. Scrolling runs no query, so it returns no
// command.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	switch key.String() {
	case "up":
		m = m.scroll(-1)
	case "down":
		m = m.scroll(1)
	case "pgup":
		m = m.scroll(-m.textHeight())
	case "pgdown":
		m = m.scroll(m.textHeight())
	}

	return m, nil
}

// scroll returns the pane scrolled down by n lines, or up when n is negative.
// It stops at the first line and at the last screenful.
func (m Model) scroll(n int) Model {
	m.offset = min(max(m.offset+n, 0), m.maxOffset())

	return m
}

// maxOffset is the offset that shows the last line at the bottom of the pane,
// or 0 when all lines fit.
func (m Model) maxOffset() int {
	return max(len(m.lines)-m.textHeight(), 0)
}

// textWidth is the number of columns the frame leaves for the text.
func (m Model) textWidth() int {
	w, _ := textSize(m.width, m.height)

	return w
}

// textHeight is the number of lines the frame leaves for the text.
func (m Model) textHeight() int {
	_, h := textSize(m.width, m.height)

	return h
}

// Open returns the pane showing raw, the line a row came from, with title in
// its frame. A JSON value
// is indented with two spaces. It keeps its key order and its duplicate names,
// and a string stays as written, so "\n" stays escaped. The JSON is in color.
// Any other line shows as it is.
func (m Model) Open(title, raw string) Model {
	v := jsontext.Value(raw)

	var text []string

	err := v.Indent(jsontext.WithIndent(indent))
	if err == nil {
		// colorize sanitizes, and must be the last step: the escape sequences
		// it adds would not survive termtext.Sanitize.
		text = strings.Split(colorize(v), "\n")
	} else {
		text = strings.Split(sanitizeLines(raw), "\n")
	}

	m.title = title
	m.text = text
	m.lines = wrap(text, m.textWidth())
	m.offset = 0

	return m
}

// View renders the pane: the frame, when it fits, around the text.
func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	body := m.body()
	if !framed(m.width, m.height) {
		return body
	}

	return frame(m.title, body, m.width, m.height)
}

// body renders the lines that fit the text's height, starting at the offset,
// then blank lines up to that height.
func (m Model) body() string {
	lines := make([]string, m.textHeight())
	copy(lines, m.lines[m.offset:])

	return strings.Join(lines, "\n")
}

// wrap breaks each of text's lines into lines of at most width cells. It
// keeps the leading spaces of a continuation line, so a wrapped value stays
// under its key. A width below 1 can't hold a character, so the lines stay
// whole.
func wrap(text []string, width int) []string {
	if width < 1 {
		return text
	}

	var lines []string

	for _, line := range text {
		wrapped := ansi.Hardwrap(line, width, true)
		lines = append(lines, strings.Split(wrapped, "\n")...)
	}

	return lines
}
