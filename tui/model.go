// Package tui is logi's terminal interface: a filter bar, a table of rows,
// newest first, a detail pane that shows one row's JSON in place of the table,
// and a status bar. One of the bar and the table has focus and takes the keys.
package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"

	"github.com/loderunner/logi/ingest"
	"github.com/loderunner/logi/kql"
	"github.com/loderunner/logi/tui/detail"
	"github.com/loderunner/logi/tui/filterbar"
	"github.com/loderunner/logi/tui/table"
)

const (
	// rateTick is how often the rows/s figure is recomputed.
	rateTick = 500 * time.Millisecond
	// rateWindow is the span of time the rows/s figure averages over.
	rateWindow = time.Second
)

// Options describes the input, for the status bar.
type Options struct {
	// Stdin is true when the input is a stream of unknown length.
	Stdin bool
	// FileSize is the size in bytes of the input file at startup. It is
	// unused when Stdin is true.
	FileSize int64
}

// ErrMsg reports a failure that ends the program. Send it to the program
// when reading the input or ingesting it fails.
type ErrMsg struct {
	Err error
}

// tickMsg fires every rateTick, to recompute the rows/s figure.
type tickMsg time.Time

// sample is the row count at a point in time.
type sample struct {
	at   time.Time
	rows int64
}

// focus is the pane that takes the keys.
type focus int

const (
	// focusTable is the table, and the detail pane that covers it.
	focusTable focus = iota
	// focusFilter is the filter bar.
	focusFilter
)

// keyMap is the keys that no pane handles.
type keyMap struct {
	// ForceQuit quits from every pane. Quit quits from every pane but the bar,
	// which takes q as text.
	ForceQuit, Quit key.Binding
	// Filter focuses the bar. It is disabled while the detail pane is shown,
	// which only Esc, q and Ctrl+C leave.
	Filter key.Binding
}

// Model is the root bubbletea model. It lays out the filter bar, the table,
// the detail pane and the status bar, decides which of them takes the keys,
// and passes what one pane produces, such as a filter or a row to show, to
// another.
type Model struct {
	opts Options

	keyMap keyMap

	// bar is the top line, with its error line under it when it has one. focus
	// says whether it or the table takes the keys.
	bar   filterbar.Model
	focus focus

	table table.Model
	// width is the width of the window. height is the number of lines above
	// the status bar, which the bar and the table share, as the table takes
	// what the bar leaves.
	width, height int

	// detail holds its own copy of the row it shows, so the table can keep
	// loading pages while the pane is open. showDetail is true while the pane
	// takes the table's place.
	detail     detail.Model
	showDetail bool

	// rows, bytes and eof are the progress that ingest last reported.
	// samples and rate are the rows/s figure and the history it comes from.
	rows    int64
	bytes   int64
	eof     bool
	samples []sample
	rate    float64

	// err is the failure that ended the program, if any.
	err error
}

// New returns a Model that shows the rows of source.
func New(source table.Rows, opts Options) Model {
	return Model{
		keyMap: keyMap{
			ForceQuit: key.NewBinding(key.WithKeys("ctrl+c")),
			Quit:      key.NewBinding(key.WithKeys("q")),
			Filter:    key.NewBinding(key.WithKeys("/")),
		},
		opts:    opts,
		bar:     filterbar.New(),
		table:   table.New(source),
		detail:  detail.New(),
		samples: []sample{{at: time.Now()}},
	}
}

// Err returns the error that ended the program, if any.
func (m Model) Err() error {
	return m.err
}

// Init implements tea.Model. It starts the ticker for the rows/s figure.
func (m Model) Init() tea.Cmd {
	return tea.Tick(rateTick, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.key(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = max(msg.Height-1, 0) // the status bar takes a line
		m.bar = m.bar.SetWidth(m.width)

		return m.layout()

	case ingest.Ingested:
		m.rows = msg.Rows
		m.bytes = msg.Bytes
		m.eof = msg.EOF
		m.samples = append(m.samples, sample{at: time.Now(), rows: msg.Rows})

		return m.updateTable(msg)

	case table.PageMsg:
		return m.updateTable(msg)

	case table.OpenMsg:
		m.detail = m.detail.Open(
			"Detail: "+table.FormatTime(msg.Row.TS),
			msg.Row.Raw,
		)

		return m.setShowDetail(true), nil

	case detail.CloseMsg:
		return m.setShowDetail(false), nil

	case filterbar.SubmitMsg:
		// A key typed after Enter changed the text before the message came
		// back. The text on screen is the one to apply, at the next Enter.
		if msg.Text != m.bar.Value() {
			return m, nil
		}

		return m.applyFilter(msg.Text)

	case filterbar.CancelMsg:
		m.bar = m.bar.Blur()
		m.focus = focusTable

		// Esc cleared the error, if any, which gives its line back.
		return m.layout()

	case tickMsg:
		m = m.updateRate(time.Time(msg))

		return m, tea.Tick(
			rateTick,
			func(t time.Time) tea.Msg { return tickMsg(t) },
		)

	case table.ErrMsg:
		return m.fail(msg.Err)

	case ErrMsg:
		return m.fail(msg.Err)

	default:
		// The bar's input sends itself messages, such as the text of a
		// clipboard paste. They come back here, and so does a terminal paste,
		// which the bar ignores without focus.
		return m.updateBar(msg)
	}
}

// key routes a key press, in this order:
//
//  1. Ctrl+C quits, whichever pane has focus.
//  2. While the bar has focus, it captures every other key, because q and /
//     are text there.
//  3. The keys of the root model: q quits, and / focuses the bar.
//  4. The pane on screen takes the rest: the detail pane while it's shown,
//     and the table otherwise.
func (m Model) key(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keyMap.ForceQuit):
		return m.quit()
	case m.focus == focusFilter:
		return m.updateBar(msg)
	case key.Matches(msg, m.keyMap.Quit):
		return m.quit()
	case key.Matches(msg, m.keyMap.Filter):
		var cmd tea.Cmd

		m.bar, cmd = m.bar.Focus()
		m.focus = focusFilter

		return m, cmd
	case m.showDetail:
		var cmd tea.Cmd

		m.detail, cmd = m.detail.Update(msg)

		return m, cmd
	default:
		return m.updateTable(msg)
	}
}

// setShowDetail shows or hides the detail pane, in place of the table. The
// pane is modal, so / doesn't leave it for the bar.
func (m Model) setShowDetail(show bool) Model {
	m.showDetail = show
	m.keyMap.Filter.SetEnabled(!show)

	return m
}

// applyFilter parses text, which the bar submitted, and when it parses,
// filters the table by it and gives the focus back to the table. An empty text
// parses to no filter, which shows every row. When it doesn't parse, the bar
// shows the error under itself, keeps focus and its text, and the filter that
// was applied stays.
func (m Model) applyFilter(text string) (Model, tea.Cmd) {
	filter, err := kql.Parse(text)
	if err != nil {
		m.bar = m.bar.SetError(err.Error())

		return m.layout()
	}

	m.bar = m.bar.Commit().Blur()
	m.focus = focusTable

	// The table loads its page again, from the same top row, so the view
	// stays where it was.
	var cmd tea.Cmd

	m.table, cmd = m.table.SetFilter(filter)

	return m, cmd
}

// updateBar sends msg to the bar and makes room for the error line that it
// may have shown or cleared.
func (m Model) updateBar(msg tea.Msg) (Model, tea.Cmd) {
	var barCmd tea.Cmd

	m.bar, barCmd = m.bar.Update(msg)

	m, layoutCmd := m.layout()

	return m, tea.Batch(barCmd, layoutCmd)
}

// updateTable sends msg to the table.
func (m Model) updateTable(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd

	m.table, cmd = m.table.Update(msg)

	return m, cmd
}

// paneHeight is the number of lines that the bar leaves for the table and the
// detail pane.
func (m Model) paneHeight() int {
	return max(m.height-m.bar.Height(), 0)
}

// layout gives the table and the detail pane the lines the bar leaves. The
// table loads its page again when its height changes.
func (m Model) layout() (Model, tea.Cmd) {
	var cmd tea.Cmd

	m.table, cmd = m.table.SetSize(m.width, m.paneHeight())
	m.detail = m.detail.SetSize(m.width, m.paneHeight())

	return m, cmd
}

// fail records err as the failure that ended the program, and ends it.
func (m Model) fail(err error) (Model, tea.Cmd) {
	m.err = err

	return m.quit()
}

// quit stops the table's query in flight and ends the program.
func (m Model) quit() (Model, tea.Cmd) {
	m.table.Stop()

	return m, tea.Quit
}

// updateRate sets the rows per second ingested over the last rateWindow
// ending at now, and prunes the samples it no longer needs. It keeps the
// newest sample at or before the start of the window as the baseline.
func (m Model) updateRate(now time.Time) Model {
	cutoff := now.Add(-rateWindow)

	i := 0
	for i+1 < len(m.samples) && !m.samples[i+1].at.After(cutoff) {
		i++
	}

	m.samples = m.samples[i:]
	base := m.samples[0]

	span := now.Sub(base.at)
	if span <= 0 {
		m.rate = 0
		return m
	}

	m.rate = float64(m.rows-base.rows) / span.Seconds()

	return m
}

// View implements tea.Model. It draws nothing until a WindowSizeMsg gives the
// model a size.
func (m Model) View() tea.View {
	var v tea.View

	v.AltScreen = true

	if m.width <= 0 || m.paneHeight() <= 0 {
		return v
	}

	status := m.status()
	status = ansi.Truncate(status, m.width, "…")
	status = lipgloss.NewStyle().
		Background(lipgloss.ANSIColor(ansi.Cyan)).
		Foreground(lipgloss.ANSIColor(ansi.Black)).
		Bold(true).
		Width(m.width).
		Render(status)

	pane := m.table.View()
	if m.showDetail {
		pane = m.detail.View()
	}

	v.SetContent(m.bar.View() + "\n" + pane + "\n" + status)

	// The bar is the top line, so its cursor needs no offset.
	v.Cursor = m.bar.Cursor()

	return v
}

// status returns the text of the status bar.
func (m Model) status() string {
	parts := []string{
		formatCount(m.rows) + " rows",
		formatCount(int64(m.rate+0.5)) + " rows/s",
		m.progress(),
		m.follow(),
	}

	return strings.Join(parts, " · ")
}

// printer groups digits as English does, whatever the locale of the terminal.
var printer = message.NewPrinter(language.English)

// follow returns the status bar's word for whether the view follows the newest
// row or stays where it was scrolled.
func (m Model) follow() string {
	if m.table.Following() {
		return "following"
	}

	return "paused"
}

// progress describes how much of the input is loaded.
func (m Model) progress() string {
	switch {
	case m.eof:
		return "EOF"
	case m.opts.Stdin:
		return "reading"
	case m.opts.FileSize <= 0:
		// An empty file has nothing left to load.
		return printer.Sprint(number.Percent(1.0))
	default:
		// The file may have grown past its starting size.
		loaded := min(float64(m.bytes)/float64(m.opts.FileSize), 1)

		return printer.Sprint(number.Percent(loaded))
	}
}

// formatCount formats n with English digit grouping, such as 1,234,567.
func formatCount(n int64) string {
	return printer.Sprint(number.Decimal(n))
}
