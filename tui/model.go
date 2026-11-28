// Package tui is logi's terminal interface: a filter bar, a table of rows,
// newest first, a detail pane that shows one row's JSON in place of the table,
// and a status bar. One of the bar and the table has focus and takes the keys.
package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"

	"github.com/loderunner/logi/ingest"
	"github.com/loderunner/logi/kql"
	"github.com/loderunner/logi/logstore"
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

// Model is the root bubbletea model. It lays out the filter bar, the table,
// the detail pane and the status bar, decides which of them takes the keys,
// and passes what one pane produces, such as a filter or a row to show, to
// another.
type Model struct {
	opts Options

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
		return m.openDetail(msg.Row), nil

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
		// What the bar sends itself, such as a paste, comes back here.
		return m.updateBar(msg)
	}
}

// key routes a key press. Ctrl+C always quits. The bar takes every other key
// while it has focus, q included, except Enter, which applies its text and
// leaves the bar unless the text is wrong, and Esc, which leaves it. The
// detail pane takes every key but Esc and q, and `/` does nothing there. The
// table has the rest.
func (m Model) key(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m.quit()
	}

	if m.focus == focusFilter {
		switch msg.String() {
		case "enter":
			return m.applyFilter()
		case "esc":
			m.bar = m.bar.Revert().Blur()
			m.focus = focusTable

			return m.layout()
		}

		return m.updateBar(msg)
	}

	// While the detail pane covers the table, it gets every other key.
	if m.showDetail {
		switch msg.String() {
		case "esc":
			return m.closeDetail(), nil
		case "q":
			return m.quit()
		}

		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)

		return m, cmd
	}

	switch msg.String() {
	case "/":
		var cmd tea.Cmd

		m.bar, cmd = m.bar.Focus()
		m.focus = focusFilter

		return m, cmd
	case "q":
		return m.quit()
	}

	return m.updateTable(msg)
}

// applyFilter parses the bar's text and, when it parses, filters the table by
// it. An empty text parses to no filter, which shows every row. When it
// doesn't parse, the bar shows the error under itself, keeps focus and its
// text, and the filter that was applied stays.
func (m Model) applyFilter() (Model, tea.Cmd) {
	filter, err := kql.Parse(m.bar.Value())
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

// openDetail opens the detail pane on row.
func (m Model) openDetail(row logstore.Row) Model {
	m.detail = m.detail.Open("Detail: "+table.FormatTime(row.TS), row.Raw)
	m.showDetail = true

	return m
}

// closeDetail closes the detail pane and brings back the table. It does
// nothing when the pane is closed.
func (m Model) closeDetail() Model {
	m.showDetail = false

	return m
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
