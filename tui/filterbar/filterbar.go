// Package filterbar is the one-line filter input above the table, with an
// error line under it when the filter text is wrong.
package filterbar

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/loderunner/logi/tui/termtext"
)

const (
	// prompt comes before the text.
	prompt = "/ "
	// placeholder shows what a filter looks like while the bar is empty.
	placeholder = "field:value"
)

// Model is the filter bar. It holds the text being edited, the text at the
// last Commit, and the message of the error under the bar, if any. A setter
// returns a changed copy, and Update handles the keys that edit, submit and
// cancel the text.
type Model struct {
	keyMap keyMap
	input  textinput.Model
	// width is the width of the whole bar. The input's own width counts the
	// text only.
	width int
	// committed is the text at the last Commit, which Esc restores.
	committed string
	// err is the message under the bar, or "" when there is none.
	err string
}

// keyMap is the keys the bar handles on top of the input's own.
type keyMap struct {
	// Submit asks to apply the text, and Cancel to leave it.
	Submit, Cancel key.Binding
}

// SubmitMsg asks the parent to apply Text, the bar's text when Enter was
// pressed. The parent calls Commit when it accepts it, or SetError when it
// doesn't.
type SubmitMsg struct {
	Text string
}

// CancelMsg tells the parent that Esc reverted the bar to its committed text,
// and asks it to move the focus away.
type CancelMsg struct{}

// New returns an empty bar that doesn't have focus.
func New() Model {
	input := textinput.New()
	input.Prompt = prompt
	input.Placeholder = placeholder

	// The terminal draws and blinks a real cursor, so the bar has no blink
	// timer to keep running, and the cursor stays right with wide characters.
	input.SetVirtualCursor(false)

	plain := lipgloss.NewStyle()
	faint := lipgloss.NewStyle().Faint(true)

	styles := input.Styles()
	styles.Focused.Prompt = plain.Bold(true)
	styles.Focused.Text = plain
	styles.Focused.Placeholder = faint
	styles.Blurred.Prompt = faint
	styles.Blurred.Text = plain
	styles.Blurred.Placeholder = faint
	input.SetStyles(styles)

	return Model{
		keyMap: keyMap{
			Submit: key.NewBinding(key.WithKeys("enter")),
			Cancel: key.NewBinding(key.WithKeys("esc")),
		},
		input: input,
	}
}

// SetWidth returns the bar resized to w columns.
func (m Model) SetWidth(w int) Model {
	m.width = w

	// The view pads the text to its width plus one column, for the cursor
	// after the last character. A width of 0 or less means no limit, which
	// is wrong for a bar too narrow for its prompt, so View cuts the line.
	m.input.SetWidth(w - lipgloss.Width(prompt) - 1)

	return m
}

// Focus returns the bar with focus, and the command that starts its cursor.
func (m Model) Focus() (Model, tea.Cmd) {
	cmd := m.input.Focus()

	return m, cmd
}

// Blur returns the bar without focus. The text stays.
func (m Model) Blur() Model {
	m.input.Blur()

	return m
}

// Value returns the text in the bar.
func (m Model) Value() string {
	return m.input.Value()
}

// Commit returns the bar with its text recorded as the one that Esc restores.
func (m Model) Commit() Model {
	m.committed = m.input.Value()

	return m
}

// revert returns the bar with the text of the last Commit, or empty before
// the first, and no error.
func (m Model) revert() Model {
	m.input.SetValue(m.committed)
	m.input.CursorEnd()
	m.err = ""

	return m
}

// SetError returns the bar with msg in a line under it, or with no such line
// when msg is empty. The next change to the text clears it.
func (m Model) SetError(msg string) Model {
	m.err = msg

	return m
}

// Height is the number of lines View takes: 1, or 2 while an error shows.
func (m Model) Height() int {
	if m.err != "" {
		return 2
	}

	return 1
}

// Update handles the keys that edit the text and move the cursor, and the
// messages that the input sends itself. A change to the text clears the error.
// Enter yields a SubmitMsg, and Esc reverts the text and yields a CancelMsg.
// The bar ignores every message while it doesn't have focus.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !m.input.Focused() {
		return m, nil
	}

	press, ok := msg.(tea.KeyPressMsg)
	switch {
	case ok && key.Matches(press, m.keyMap.Submit):
		text := m.input.Value()

		return m, func() tea.Msg { return SubmitMsg{Text: text} }
	case ok && key.Matches(press, m.keyMap.Cancel):
		return m.revert(), func() tea.Msg { return CancelMsg{} }
	}

	before := m.input.Value()

	var cmd tea.Cmd

	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		m.err = ""
	}

	return m, cmd
}

// View draws the bar, then the error line when there is one. Each line is
// cut to the width of the bar.
func (m Model) View() string {
	line := m.cut(m.input.View())
	if m.err == "" {
		return line
	}

	msg := lipgloss.NewStyle().
		Foreground(lipgloss.ANSIColor(ansi.Red)).
		Render(termtext.Sanitize(m.err))

	return line + "\n" + m.cut(msg)
}

// Cursor returns the terminal cursor for the bar, in the bar's own
// coordinates, or nil while the bar doesn't have focus.
func (m Model) Cursor() *tea.Cursor {
	return m.input.Cursor()
}

// cut cuts s to the width of the bar. It does nothing before the width is
// known.
func (m Model) cut(s string) string {
	if m.width <= 0 {
		return s
	}

	return ansi.Truncate(s, m.width, "…")
}
