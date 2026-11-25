// Package tui is logi's terminal interface: a table of rows, newest first,
// a detail pane that shows one row's JSON in place of the table, and a status
// bar.
package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"

	"github.com/loderunner/logi/ingest"
	"github.com/loderunner/logi/logstore"
	"github.com/loderunner/logi/tui/detail"
	"github.com/loderunner/logi/tui/table"
)

const (
	// rateTick is how often the rows/s figure is recomputed.
	rateTick = 500 * time.Millisecond
	// rateWindow is the span of time the rows/s figure averages over.
	rateWindow = time.Second
)

// Rows is where the TUI gets rows to show. *logstore.Store satisfies it.
type Rows interface {
	// Page returns a page of rows, newest first, each with the value of every
	// key in the query.
	Page(ctx context.Context, q logstore.PageQuery) (logstore.Page, error)
}

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

// pageMsg carries the result of a page query.
type pageMsg struct {
	gen  int
	page logstore.Page
}

// tickMsg fires every rateTick, to recompute the rows/s figure.
type tickMsg time.Time

// sample is the row count at a point in time.
type sample struct {
	at   time.Time
	rows int64
}

// Model is the root bubbletea model: the table, the detail pane, the status bar
// and the page queries that fill the table.
type Model struct {
	source Rows
	opts   Options

	table         table.Model
	width, height int

	// detail holds its own copy of the row it shows, so the table can keep
	// loading pages while the pane is open. showDetail is true while the pane
	// takes the table's place.
	detail     detail.Model
	showDetail bool

	// known lists every key seen so far, in order of appearance. timeKey is
	// the one the timestamp came from, or "" until ingest has found it. keys
	// are the table's columns after the timestamp, as defaultKeys picks them.
	known   []string
	timeKey string
	keys    []string

	// from is the key of the top row on screen, or nil while the table
	// follows the newest row. It comes from the last page and is never
	// changed in place, so copies of the model share it.
	from *logstore.Key
	// move is the number of rows the keys have asked to scroll since the last
	// page arrived: positive is older, negative is newer. Each query repeats
	// it, so a query that a newer one replaces loses no key press.
	move int

	// gen numbers page queries. cancel stops the one in flight. A result
	// with an older gen is dropped.
	gen    int
	cancel context.CancelFunc

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
func New(source Rows, opts Options) Model {
	return Model{
		source:  source,
		opts:    opts,
		table:   table.New(),
		detail:  detail.New(),
		keys:    defaultKeys(nil, ""),
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
		switch msg.String() {
		case "q", "ctrl+c":
			return m.quit()
		case "esc":
			return m.closeDetail(), nil
		}

		// While the detail pane covers the table, it gets every other key.
		if m.showDetail {
			var cmd tea.Cmd
			m.detail, cmd = m.detail.Update(msg)

			return m, cmd
		}

		switch msg.String() {
		case "enter":
			return m.openDetail(), nil
		case "up":
			return m.scroll(-1)
		case "down":
			return m.scroll(1)
		case "pgup":
			return m.scroll(-m.height)
		case "pgdown":
			return m.scroll(m.height)
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = max(msg.Height-1, 0) // the status bar takes a line
		m.table = m.table.SetSize(m.width, m.height)
		m.detail = m.detail.SetSize(m.width, m.height)

		return m.refresh()

	case ingest.Ingested:
		m.rows = msg.Rows
		m.bytes = msg.Bytes
		m.eof = msg.EOF
		m.samples = append(m.samples, sample{at: time.Now(), rows: msg.Rows})
		m = m.setColumns(msg.NewKeys, msg.TimeKey)

		return m.refresh()

	case pageMsg:
		if msg.gen != m.gen {
			return m, nil
		}

		m.from = msg.page.From
		m.move = 0
		m.table = m.table.SetRows(msg.page.Rows)

	case tickMsg:
		m = m.updateRate(time.Time(msg))

		return m, tea.Tick(
			rateTick,
			func(t time.Time) tea.Msg { return tickMsg(t) },
		)

	case ErrMsg:
		m.err = msg.Err

		return m.quit()
	}

	return m, nil
}

// openDetail opens the detail pane on the selected row. An empty table has
// none, so nothing opens.
func (m Model) openDetail() Model {
	row, ok := m.table.Selected()
	if !ok {
		return m
	}

	m.detail = m.detail.Open(row.Raw)
	m.showDetail = true

	return m
}

// closeDetail closes the detail pane and brings back the table. It does
// nothing when the pane is closed.
func (m Model) closeDetail() Model {
	m.showDetail = false

	return m
}

// quit stops the in-flight query and ends the program.
func (m Model) quit() (tea.Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
	}

	return m, tea.Quit
}

// scroll asks to move the view by n rows, positive for older rows, and
// refreshes the page.
func (m Model) scroll(n int) (tea.Model, tea.Cmd) {
	m.move += n

	return m.refresh()
}

// refresh cancels the in-flight page query and starts a new one. The query
// starts from the top row of the last page that arrived, and asks for all the
// scrolling since then.
func (m Model) refresh() (tea.Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}

	if m.height <= 0 {
		return m, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.gen++

	// The Cmd runs after Update returns, and m.keys may change by then.
	keys := slices.Clone(m.keys)

	q := logstore.PageQuery{
		From:  m.from,
		Move:  m.move,
		Limit: m.height,
		Keys:  keys,
	}

	return m, queryPage(ctx, m.source, q, m.gen)
}

// setColumns records newly seen keys and the time key and, when they change
// the default columns, switches the table to them. The refresh that follows
// loads rows for the new keys, and its generation number drops results for
// the old ones.
func (m Model) setColumns(newKeys []string, timeKey string) Model {
	m.known = append(m.known, newKeys...)
	m.timeKey = timeKey

	keys := defaultKeys(m.known, m.timeKey)
	if slices.Equal(keys, m.keys) {
		return m
	}

	m.keys = keys
	m.table = m.table.
		SetMessageColumn(messageIndex(keys)).
		SetLevelColumn(levelIndex(keys)).
		ResetWidths()

	return m
}

// queryPage loads the page q asks for. It reports a failure as an ErrMsg,
// except when ctx was canceled, which means a newer query replaced it or the
// program is quitting. The driver may not wrap context.Canceled, so the check
// is on ctx, not on the error.
func queryPage(
	ctx context.Context,
	source Rows,
	q logstore.PageQuery,
	gen int,
) tea.Cmd {
	return func() tea.Msg {
		page, err := source.Page(ctx, q)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return ErrMsg{Err: fmt.Errorf("load rows: %w", err)}
		}

		return pageMsg{gen: gen, page: page}
	}
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

	if m.width <= 0 || m.height <= 0 {
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

	v.SetContent(pane + "\n" + status)

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
	if m.from == nil {
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
