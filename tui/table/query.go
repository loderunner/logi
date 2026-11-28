package table

import (
	"context"
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/loderunner/logi/logstore"
)

// Rows is where the table gets its pages. *logstore.Store satisfies it.
type Rows interface {
	// Page returns a page of rows, newest first, each with the value of every
	// key in the query.
	Page(ctx context.Context, q logstore.PageQuery) (logstore.Page, error)
}

// PageMsg carries the result of a page query back to the table. The parent
// routes it to Update.
type PageMsg struct {
	gen  int
	page logstore.Page
}

// ErrMsg reports a page query that failed. The table can't recover from it,
// so the parent ends the program.
type ErrMsg struct {
	Err error
}

// refresh cancels the in-flight page query and starts a new one. The query
// starts from the top row of the last page that arrived, and asks for all the
// scrolling since then. The generation number drops the pages of every
// earlier query, which may have used other keys, another filter or another
// height.
func (m Model) refresh() (Model, tea.Cmd) {
	m.Stop()
	m.cancel = nil

	if m.height <= 0 {
		return m, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.gen++

	q := logstore.PageQuery{
		From:  m.from,
		Move:  m.move,
		Limit: m.height,
		// The Cmd runs after Update returns, and m.columns may change by then.
		Keys: slices.Clone(m.columns),
		// A filter is immutable, so the Cmd can share it.
		Filter: m.filter,
	}

	return m, queryPage(ctx, m.source, q, m.gen)
}

// Stop cancels the page query in flight, if any. Call it when the program
// quits.
func (m Model) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
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

		return PageMsg{gen: gen, page: page}
	}
}
