// Package tui is logi's terminal interface: a table of the newest rows and a
// status bar.
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
	// Newest returns up to limit rows, newest first, each with the value of
	// every key in keys.
	Newest(
		ctx context.Context,
		limit int,
		keys []string,
	) ([]logstore.Row, error)
}

// Options describes the input, for the status bar.
type Options struct {
	// Stdin is true when the input is a stream of unknown length. Otherwise
	// the input is a file of FileSize bytes at startup.
	Stdin    bool
	FileSize int64
}

// ErrMsg reports a failure that ends the program. Send it to the program
// when ingest fails.
type ErrMsg struct {
	Err error
}

// rowsMsg carries the result of a window query.
type rowsMsg struct {
	gen  int
	rows []logstore.Row
}

type tickMsg time.Time

// sample is the row count at a point in time.
type sample struct {
	at   time.Time
	rows int64
}

// Model is the root model.
type Model struct {
	source Rows
	opts   Options

	table         table.Model
	width, height int

	// known lists every key seen so far, in order of appearance. timeKey is
	// the one the timestamp came from, or "" until ingest has found it. keys
	// is the subset the table shows.
	known   []string
	timeKey string
	keys    []string

	// gen numbers window queries. cancel stops the one in flight. A result
	// with an older gen is dropped.
	gen    int
	cancel context.CancelFunc

	rows    int64
	bytes   int64
	eof     bool
	samples []sample
	rate    float64

	err error
}

// New returns the root model.
func New(source Rows, opts Options) Model {
	return Model{
		source:  source,
		opts:    opts,
		table:   table.New(),
		keys:    defaultKeys(nil, ""),
		samples: []sample{{at: time.Now()}},
	}
}

// Err returns the error that ended the program, if any.
func (m Model) Err() error {
	return m.err
}

// Init implements tea.Model.
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
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = max(msg.Height-1, 0) // the status bar takes a line
		m.table = m.table.SetSize(m.width, m.height)

		return m.refresh()

	case ingest.Ingested:
		m.rows = msg.Rows
		m.bytes = msg.Bytes
		m.eof = msg.EOF
		m.samples = append(m.samples, sample{at: time.Now(), rows: msg.Rows})
		m = m.setColumns(msg.NewKeys, msg.TimeKey)

		return m.refresh()

	case rowsMsg:
		if msg.gen != m.gen {
			return m, nil
		}

		m.table = m.table.SetRows(msg.rows)

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

// quit stops the in-flight query and ends the program.
func (m Model) quit() (tea.Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
	}

	return m, tea.Quit
}

// refresh cancels the in-flight window query and starts a new one.
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

	return m, queryRows(ctx, m.source, m.height, keys, m.gen)
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
	m.table = m.table.SetMessageColumn(messageIndex(keys)).ResetWidths()

	return m
}

// queryRows loads the newest limit rows, with a value for each of keys. It
// reports a failure as an ErrMsg, except when ctx was canceled, which means a
// newer query replaced it or the program is quitting. The driver may not wrap
// context.Canceled, so the check is on ctx, not on the error.
func queryRows(
	ctx context.Context,
	source Rows,
	limit int,
	keys []string,
	gen int,
) tea.Cmd {
	return func() tea.Msg {
		rows, err := source.Newest(ctx, limit, keys)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return ErrMsg{Err: fmt.Errorf("load rows: %w", err)}
		}

		return rowsMsg{gen: gen, rows: rows}
	}
}

// updateRate sets the rows per second ingested over the last rateWindow
// ending at now, and prunes the samples it no longer needs. It keeps the
// newest sample older than the window as the baseline.
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

// View implements tea.Model.
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

	v.SetContent(m.table.View() + "\n" + status)

	return v
}

// status returns the text of the status bar.
func (m Model) status() string {
	parts := []string{
		formatCount(m.rows) + " rows",
		formatCount(int64(m.rate+0.5)) + " rows/s",
		m.progress(),
	}

	return strings.Join(parts, " · ")
}

var printer = message.NewPrinter(language.English)

// progress describes how much of the input is loaded.
func (m Model) progress() string {
	switch {
	case m.eof:
		return "EOF"
	case m.opts.Stdin:
		return "reading"
	case m.opts.FileSize <= 0:
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
