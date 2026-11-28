package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/loderunner/logi/ingest"
	"github.com/loderunner/logi/logstore"
	table_mock "github.com/loderunner/logi/tui/table/mock_"
)

// update applies msg and returns the resulting Model and command, without
// running the command.
func update(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()

	next, cmd := m.Update(msg)

	model, ok := next.(Model)
	require.True(t, ok)

	return model, cmd
}

// send applies msg, then runs the command it returns and applies each message
// that comes out, and so on, as the program would. It runs the page queries
// too, so the mock must expect them. It returns the model and whether a
// command quit the program.
func send(t *testing.T, m Model, msg tea.Msg) (Model, bool) {
	t.Helper()

	m, cmd := update(t, m, msg)

	return run(t, m, cmd)
}

// run runs cmd and applies the messages it yields, as send does.
func run(t *testing.T, m Model, cmd tea.Cmd) (Model, bool) {
	t.Helper()

	if cmd == nil {
		return m, false
	}

	switch msg := cmd().(type) {
	case nil:
		return m, false
	case tea.QuitMsg:
		return m, true
	case tea.BatchMsg:
		quit := false

		for _, cmd := range msg {
			var q bool

			m, q = run(t, m, cmd)
			quit = quit || q
		}

		return m, quit
	default:
		return send(t, m, msg)
	}
}

// asks returns a source that records the query of every Page call and answers
// each with answer.
func asks(
	t *testing.T,
	answer logstore.Page,
) (*table_mock.MockRows, *[]logstore.PageQuery) {
	t.Helper()

	mockRows := table_mock.NewMockRows(gomock.NewController(t))

	var asked []logstore.PageQuery

	mockRows.EXPECT().
		Page(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, q logstore.PageQuery) (logstore.Page, error) {
			asked = append(asked, q)
			return answer, nil
		}).
		AnyTimes()

	return mockRows, &asked
}

// withRows returns a model of the given size whose table shows rows, newest
// first. The mock serves a page of rows to the first query, and expects no
// other.
func withRows(
	t *testing.T,
	size tea.WindowSizeMsg,
	rows ...logstore.Row,
) (Model, *table_mock.MockRows) {
	t.Helper()

	mockRows := table_mock.NewMockRows(gomock.NewController(t))
	mockRows.EXPECT().
		Page(gomock.Any(), gomock.Any()).
		Return(logstore.Page{Rows: rows}, nil)

	m, _ := send(t, New(mockRows, Options{FileSize: 100}), size)

	return m, mockRows
}

// noRows returns a model without a source, for tests that run no query.
func noRows(t *testing.T, opts Options) Model {
	t.Helper()

	return New(table_mock.NewMockRows(gomock.NewController(t)), opts)
}

func TestModel(t *testing.T) {
	// The bar and the status bar take two of the 7 lines, which leaves a table
	// 5 rows high.
	size := tea.WindowSizeMsg{Width: 40, Height: 7}

	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	esc := tea.KeyPressMsg{Code: tea.KeyEscape}
	q := tea.KeyPressMsg{Code: 'q', Text: "q"}
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

	t.Run("Update", func(t *testing.T) {
		t.Run("Ingested fills the table", func(t *testing.T) {
			mockRows := table_mock.NewMockRows(gomock.NewController(t))
			mockRows.EXPECT().
				Page(gomock.Any(), logstore.PageQuery{Limit: 5, Keys: []string{"_raw"}}).
				Return(logstore.Page{Rows: []logstore.Row{{ID: 2, Raw: "newer", Values: []string{""}}, {ID: 1, Raw: "older", Values: []string{""}}}}, nil).
				Times(2)

			m, _ := send(t, New(mockRows, Options{FileSize: 100}), size)
			m, _ = send(t, m, ingest.Ingested{Rows: 2, Bytes: 10})

			view := ansi.Strip(m.View().Content)

			// The top row is padded to the width, so it ends in spaces.
			require.Contains(t, view, "0001-01-01 00:00:00.000 newer ")
			require.Contains(t, view, "\n0001-01-01 00:00:00.000 older\n")
			require.Contains(t, view, "2 rows")
		})

		t.Run(
			"a failed query ends the program with its error",
			func(t *testing.T) {
				mockRows := table_mock.NewMockRows(gomock.NewController(t))
				boom := errors.New("boom")
				mockRows.EXPECT().
					Page(gomock.Any(), gomock.Any()).
					Return(logstore.Page{}, boom)

				m, quit := send(t, New(mockRows, Options{FileSize: 100}), size)

				require.True(t, quit)
				require.ErrorIs(t, m.Err(), boom)
			},
		)

		t.Run("an ingest error ends the program", func(t *testing.T) {
			boom := errors.New("boom")

			m, quit := send(t, noRows(t, Options{}), ErrMsg{Err: boom})

			require.True(t, quit)
			require.ErrorIs(t, m.Err(), boom)
		})

		t.Run("q and ctrl+c quit", func(t *testing.T) {
			for _, key := range []tea.KeyPressMsg{q, ctrlC} {
				_, quit := send(t, noRows(t, Options{}), key)
				require.True(t, quit, key.String())
			}
		})

		t.Run("quitting stops the query in flight", func(t *testing.T) {
			mockRows := table_mock.NewMockRows(gomock.NewController(t))

			var queryCtx context.Context

			mockRows.EXPECT().
				Page(gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, _ logstore.PageQuery) (logstore.Page, error) {
					queryCtx = ctx
					return logstore.Page{}, nil
				})

			m, query := update(t, New(mockRows, Options{}), size)
			_, _ = update(t, m, q)

			require.Nil(t, query())
			require.ErrorIs(t, queryCtx.Err(), context.Canceled)
		})

		t.Run("the arrow and page keys scroll the table", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			m, _ := send(t, New(mockRows, Options{}), size)
			_, _ = send(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})

			require.Len(t, *asked, 2)
			require.Equal(t, 5, (*asked)[1].Move)
		})

		t.Run("other keys do nothing", func(t *testing.T) {
			_, cmd := update(
				t,
				noRows(t, Options{}),
				tea.KeyPressMsg{Code: 'x', Text: "x"},
			)
			require.Nil(t, cmd)
		})
	})

	t.Run("detail pane", func(t *testing.T) {
		newest := logstore.Row{
			ID:     2,
			TS:     time.Date(2026, 11, 25, 9, 0, 58, 405e6, time.UTC),
			Raw:    `{"msg":"hi","n":[1,2]}`,
			Values: []string{"hi"},
		}
		older := logstore.Row{
			ID:     1,
			Raw:    `{"msg":"old"}`,
			Values: []string{"old"},
		}

		// firstLine returns the first line of text in the detail pane,
		// inside its frame, under the bar and the frame's top.
		firstLine := func(m Model) string {
			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			line := strings.TrimSuffix(lines[2], "│")

			return strings.TrimRight(strings.TrimPrefix(line, "│ "), " ")
		}

		t.Run("Enter opens the top row's JSON", func(t *testing.T) {
			m, _ := withRows(t, size, newest, older)

			m, _ = send(t, m, enter)
			require.True(t, m.showDetail)

			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			require.Equal(t, []string{
				"/ field:value                           ",
				"╭─ Detail: 2026-11-25 09:00:58.405 ────╮",
				"│ {                                    │",
				`│   "msg": "hi",                       │`,
				`│   "n": [                             │`,
				"╰──────────────────────────────────────╯",
			}, lines[:6])
		})

		t.Run("Esc brings back the table", func(t *testing.T) {
			m, _ := withRows(t, size, newest, older)
			m, _ = send(t, m, enter)

			m, _ = send(t, m, esc)
			require.False(t, m.showDetail)

			view := ansi.Strip(m.View().Content)
			require.Contains(t, view, "2026-11-25 09:00:58.405 hi")
			require.NotContains(t, view, `"msg"`)
		})

		t.Run("shows a line that isn't JSON without control characters",
			func(t *testing.T) {
				m, _ := withRows(t, size, logstore.Row{
					ID:     1,
					Raw:    "not json\t\x1b[31mred",
					Values: []string{""},
				})

				m, _ = send(t, m, enter)

				require.Equal(t, "not json    �[31mred", firstLine(m))
			},
		)

		t.Run("Enter on an empty table does nothing", func(t *testing.T) {
			m, _ := withRows(t, size)

			m, _ = send(t, m, enter)
			require.False(t, m.showDetail)
		})

		t.Run("Esc with the table showing does nothing", func(t *testing.T) {
			m, _ := withRows(t, size, newest)

			m, cmd := update(t, m, esc)
			require.Nil(t, cmd)
			require.False(t, m.showDetail)
		})

		t.Run("the arrow and page keys scroll the pane", func(t *testing.T) {
			// The pane shows 3 lines inside its frame, and the mock expects
			// one Page call, for the first query, so scrolling the pane
			// doesn't scroll the table.
			m, _ := withRows(t, size, logstore.Row{
				ID:     1,
				Raw:    `{"a":[1,2,3,4,5,6,7,8]}`,
				Values: []string{""},
			})
			m, _ = send(t, m, enter)

			require.Equal(t, "{", firstLine(m))

			for _, step := range []struct {
				key  rune
				want string
			}{
				{tea.KeyDown, `  "a": [`},
				{tea.KeyPgDown, "    3,"},
				{tea.KeyUp, "    2,"},
				{tea.KeyPgDown, "    5,"},
				{tea.KeyPgDown, "    8"},
				{tea.KeyPgDown, "    8"},
				{tea.KeyPgUp, "    5,"},
				{tea.KeyPgUp, "    2,"},
				{tea.KeyPgUp, "{"},
			} {
				m, _ = send(t, m, tea.KeyPressMsg{Code: step.key})
				require.True(t, m.showDetail)
				require.Equal(t, step.want, firstLine(m))
			}
		})

		t.Run("q and ctrl+c quit", func(t *testing.T) {
			for _, key := range []tea.KeyPressMsg{q, ctrlC} {
				m, _ := withRows(t, size, newest)
				m, _ = send(t, m, enter)

				_, quit := send(t, m, key)
				require.True(t, quit, key.String())
			}
		})

		t.Run("Ingested still updates the table", func(t *testing.T) {
			m, mockRows := withRows(t, size, newest, older)
			m, _ = send(t, m, enter)

			fresh := logstore.Row{
				ID:     3,
				Raw:    `{"msg":"fresh"}`,
				Values: []string{"fresh"},
			}
			mockRows.EXPECT().
				Page(gomock.Any(), gomock.Any()).
				Return(logstore.Page{Rows: []logstore.Row{fresh, newest}}, nil)

			m, _ = send(t, m, ingest.Ingested{Rows: 3})

			// The pane still shows the row it opened on.
			require.Contains(t, ansi.Strip(m.View().Content), `"hi"`)

			m, _ = send(t, m, esc)
			require.Contains(
				t,
				ansi.Strip(m.View().Content),
				"0001-01-01 00:00:00.000 fresh",
			)
		})

		t.Run("a resize resizes the pane", func(t *testing.T) {
			m, _ := withRows(t, size, newest)
			m, _ = send(t, m, enter)

			m, _ = send(t, m, tea.WindowSizeMsg{Width: 8, Height: 7})

			require.Contains(
				t,
				ansi.Strip(m.View().Content),
				"  \"msg\":\n",
			)
		})
	})

	t.Run("rows per second", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			m := noRows(t, Options{})

			time.Sleep(500 * time.Millisecond)

			m, _ = update(t, m, ingest.Ingested{Rows: 4000})
			m, cmd := update(t, m, tickMsg(time.Now()))
			require.NotNil(t, cmd, "the tick re-arms itself")
			require.InDelta(t, 8000, m.rate, 1)

			// Ingest goes quiet: the burst leaves the one-second window.
			time.Sleep(2 * time.Second)

			m, _ = update(t, m, tickMsg(time.Now()))
			require.Zero(t, m.rate)
		})
	})

	t.Run("status", func(t *testing.T) {
		t.Run("a file shows the percentage loaded", func(t *testing.T) {
			m := noRows(t, Options{FileSize: 200})
			m.rows = 12345
			m.bytes = 84
			m.rate = 8000

			require.Equal(
				t,
				"12,345 rows · 8,000 rows/s · 42% · following",
				m.status(),
			)
		})

		t.Run("a file caps the percentage at 100", func(t *testing.T) {
			m := noRows(t, Options{FileSize: 100})
			m.bytes = 250

			require.Equal(t, "100%", m.progress())
		})

		t.Run("an empty file is fully loaded", func(t *testing.T) {
			m := noRows(t, Options{})

			require.Equal(t, "100%", m.progress())
		})

		t.Run(
			"shows following until the view scrolls away",
			func(t *testing.T) {
				mockRows, _ := asks(
					t,
					logstore.Page{From: &logstore.Key{ID: 5}},
				)

				m, _ := update(t, New(mockRows, Options{Stdin: true}), size)
				require.Equal(
					t,
					"0 rows · 0 rows/s · reading · following",
					m.status(),
				)

				m, _ = send(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
				require.Equal(
					t,
					"0 rows · 0 rows/s · reading · paused",
					m.status(),
				)
			},
		)

		t.Run("stdin shows reading, then EOF", func(t *testing.T) {
			m := noRows(t, Options{Stdin: true})
			require.Equal(t, "reading", m.progress())

			m, _ = update(t, m, ingest.Ingested{Rows: 3, EOF: true})
			require.Equal(t, "EOF", m.progress())
			require.Equal(t, "3 rows · 0 rows/s · EOF · following", m.status())
		})
	})

	t.Run("filter bar", func(t *testing.T) {
		slash := tea.KeyPressMsg{Code: '/', Text: "/"}
		onlyErrors := logstore.Match{Key: "level", Value: "error"}

		// typed returns m after the keys that type text.
		typed := func(t *testing.T, m Model, text string) Model {
			t.Helper()

			for _, r := range text {
				m, _ = send(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
			}

			return m
		}

		// withBar returns a model of the given size with a mock that
		// records every page query, and the table showing no rows.
		withBar := func(
			t *testing.T,
			size tea.WindowSizeMsg,
		) (Model, *[]logstore.PageQuery) {
			t.Helper()

			mockRows, asked := asks(t, logstore.Page{})
			m, _ := send(t, New(mockRows, Options{FileSize: 100}), size)

			return m, asked
		}

		// lastQuery returns the newest query that the mock saw.
		lastQuery := func(asked *[]logstore.PageQuery) logstore.PageQuery {
			return (*asked)[len(*asked)-1]
		}

		// cleared returns m after the keys that delete all the text in the bar.
		cleared := func(t *testing.T, m Model) Model {
			t.Helper()

			for range len(m.bar.Value()) {
				m, _ = send(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
			}

			require.Empty(t, m.bar.Value())

			return m
		}

		// apply types text in the bar, which must not have focus, and presses
		// Enter.
		apply := func(t *testing.T, m Model, text string) Model {
			t.Helper()

			m, _ = send(t, m, slash)
			m = typed(t, m, text)
			m, _ = send(t, m, enter)

			return m
		}

		t.Run("the table has focus at startup", func(t *testing.T) {
			m, _ := withRows(t, size)

			require.Equal(t, focusTable, m.focus)
			require.Nil(t, m.View().Cursor)
		})

		t.Run(
			"shows the bar above the table and the status bar",
			func(t *testing.T) {
				m, _ := withRows(
					t,
					size,
					logstore.Row{ID: 1, Raw: "a", Values: []string{""}},
				)

				lines := strings.Split(ansi.Strip(m.View().Content), "\n")
				require.Len(t, lines, 7)
				require.Equal(
					t,
					"/ field:value                           ",
					lines[0],
				)
				require.Contains(t, lines[1], "0001-01-01 00:00:00.000 a")
				require.Contains(t, lines[6], "rows")
			},
		)

		t.Run("/ focuses the bar", func(t *testing.T) {
			m, _ := withRows(t, size)

			m, cmd := update(t, m, slash)
			require.Nil(t, cmd)
			require.Equal(t, focusFilter, m.focus)

			// The bar's cursor is on its row, after the prompt.
			cursor := m.View().Cursor
			require.NotNil(t, cursor)
			require.Equal(t, 2, cursor.X)
			require.Equal(t, 0, cursor.Y)

			// The slash itself isn't typed.
			require.Empty(t, m.bar.Value())
		})

		t.Run("types q and the other printable keys", func(t *testing.T) {
			m, _ := withRows(t, size)
			m, _ = send(t, m, slash)

			m = typed(t, m, "level:error q /+-=c")

			require.Equal(t, "level:error q /+-=c", m.bar.Value())
			require.Equal(t, focusFilter, m.focus)
		})

		t.Run("Enter keeps the text and returns focus to the table",
			func(t *testing.T) {
				m, _ := withBar(t, size)
				m = apply(t, m, "level:error")

				require.Equal(t, focusTable, m.focus)
				require.Equal(t, "level:error", m.bar.Value())
				require.Nil(t, m.View().Cursor)
				require.Contains(
					t,
					ansi.Strip(m.View().Content),
					"/ level:error",
				)

				// The table takes the keys again, so q quits.
				_, quit := send(t, m, q)
				require.True(t, quit)
			},
		)

		t.Run("Enter filters the table", func(t *testing.T) {
			m, asked := withBar(t, size)
			require.Len(t, *asked, 1)
			require.Nil(t, (*asked)[0].Filter)

			m = apply(t, m, "level:error")

			require.Len(t, *asked, 2)
			require.Equal(t, onlyErrors, lastQuery(asked).Filter)
			require.Equal(t, 1, m.bar.Height())
		})

		t.Run("an empty text removes the filter", func(t *testing.T) {
			m, asked := withBar(t, size)
			m = apply(t, m, "level:error")
			require.NotNil(t, lastQuery(asked).Filter)

			m, _ = send(t, m, slash)
			m = cleared(t, m)
			m, _ = send(t, m, enter)

			require.Len(t, *asked, 3)
			require.Nil(t, lastQuery(asked).Filter)
			require.Equal(t, focusTable, m.focus)
		})

		t.Run("a blank text removes the filter", func(t *testing.T) {
			m, asked := withBar(t, size)
			m = apply(t, m, "level:error")

			m, _ = send(t, m, slash)
			m = cleared(t, m)
			m = typed(t, m, "  ")
			_, _ = send(t, m, enter)

			require.Nil(t, lastQuery(asked).Filter)
		})

		t.Run("a text that doesn't parse shows its error and keeps focus",
			func(t *testing.T) {
				m, asked := withBar(t, size)
				m = apply(t, m, "level:error")
				require.Len(t, *asked, 2)

				m, _ = send(t, m, slash)
				m = typed(t, m, " level")
				m, _ = send(t, m, enter)

				require.Equal(t, focusFilter, m.focus)
				require.Equal(t, 2, m.bar.Height())
				require.Equal(t, "level:error level", m.bar.Value())

				// The error line takes a row from the table, and the filter
				// from before stays applied.
				require.Len(t, *asked, 3)
				require.Equal(t, 4, lastQuery(asked).Limit)
				require.Equal(t, onlyErrors, lastQuery(asked).Filter)

				view := ansi.Strip(m.View().Content)
				require.Contains(t, view, "col 13: ")
			},
		)

		t.Run("an error with no filter applied queries without one",
			func(t *testing.T) {
				m, asked := withBar(t, size)

				m, _ = send(t, m, slash)
				m = typed(t, m, "level")
				_, _ = send(t, m, enter)

				require.Equal(t, 4, lastQuery(asked).Limit)
				require.Nil(t, lastQuery(asked).Filter)
			},
		)

		t.Run("the same error again doesn't query", func(t *testing.T) {
			m, asked := withBar(t, size)

			m, _ = send(t, m, slash)
			m = typed(t, m, "level")
			m, _ = send(t, m, enter)
			require.Len(t, *asked, 2)

			m, _ = send(t, m, enter)
			require.Len(t, *asked, 2)
			require.Equal(t, focusFilter, m.focus)
		})

		t.Run("the next edit clears the error and restores the page",
			func(t *testing.T) {
				m, asked := withBar(t, size)

				m, _ = send(t, m, slash)
				m = typed(t, m, "level")
				m, _ = send(t, m, enter)
				require.Equal(t, 4, lastQuery(asked).Limit)

				m = typed(t, m, ":")

				require.Equal(t, 1, m.bar.Height())
				require.Equal(t, 5, lastQuery(asked).Limit)
				require.Equal(t, focusFilter, m.focus)
			},
		)

		t.Run("Esc after an error reverts the text and clears the error",
			func(t *testing.T) {
				m, asked := withBar(t, size)
				m = apply(t, m, "level:error")

				m, _ = send(t, m, slash)
				m = typed(t, m, " level")
				m, _ = send(t, m, enter)
				require.Equal(t, 4, lastQuery(asked).Limit)

				m, _ = send(t, m, esc)

				require.Equal(t, focusTable, m.focus)
				require.Equal(t, "level:error", m.bar.Value())
				require.Equal(t, 1, m.bar.Height())
				require.Equal(t, 5, lastQuery(asked).Limit)
				require.Equal(t, onlyErrors, lastQuery(asked).Filter)
			},
		)

		t.Run("Esc leaves the applied filter alone", func(t *testing.T) {
			m, asked := withBar(t, size)
			m = apply(t, m, "level:error")
			n := len(*asked)

			m, _ = send(t, m, slash)
			m = typed(t, m, " x")
			m, _ = send(t, m, esc)
			require.Len(t, *asked, n)

			_, _ = send(t, m, ingest.Ingested{Rows: 1})
			require.Equal(t, onlyErrors, lastQuery(asked).Filter)
		})

		t.Run("Esc reverts to the committed text", func(t *testing.T) {
			m, _ := withBar(t, size)
			m = apply(t, m, "level:error")

			m, _ = send(t, m, slash)
			m = typed(t, m, " x")
			require.Equal(t, "level:error x", m.bar.Value())

			m, _ = send(t, m, esc)
			require.Equal(t, focusTable, m.focus)
			require.Nil(t, m.View().Cursor)
			require.Equal(t, "level:error", m.bar.Value())
		})

		t.Run("Esc before any commit empties the bar", func(t *testing.T) {
			m, _ := withRows(t, size)
			m, _ = send(t, m, slash)
			m = typed(t, m, "abc")

			m, _ = send(t, m, esc)
			require.Equal(t, focusTable, m.focus)
			require.Empty(t, m.bar.Value())
		})

		t.Run("ctrl+c quits from the bar", func(t *testing.T) {
			m, _ := withRows(t, size)
			m, _ = send(t, m, slash)
			m = typed(t, m, "abc")

			_, quit := send(t, m, ctrlC)
			require.True(t, quit)
		})

		t.Run("q quits from the table, not from the bar", func(t *testing.T) {
			m, _ := withRows(t, size)

			_, quit := send(t, m, q)
			require.True(t, quit)

			m, _ = send(t, m, slash)
			_, quit = send(t, m, q)
			require.False(t, quit)
		})

		t.Run("the table's keys go to the bar while it has focus",
			func(t *testing.T) {
				m, asked := withBar(t, size)
				m, _ = send(t, m, slash)

				for _, k := range []rune{tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown} {
					m, _ = send(t, m, tea.KeyPressMsg{Code: k})
				}

				require.Len(t, *asked, 1)
			},
		)

		t.Run("a paste goes to the bar", func(t *testing.T) {
			m, _ := withRows(t, size)
			m, _ = send(t, m, slash)

			m, _ = send(t, m, tea.PasteMsg{Content: "level:error"})
			require.Equal(t, "level:error", m.bar.Value())
		})

		t.Run(
			"a paste is ignored while the table has focus",
			func(t *testing.T) {
				m, _ := withRows(t, size)

				m, _ = send(t, m, tea.PasteMsg{Content: "level:error"})
				require.Empty(t, m.bar.Value())
			},
		)

		t.Run("/ does nothing in the detail pane", func(t *testing.T) {
			m, _ := withRows(t, size, logstore.Row{
				ID:     1,
				Raw:    `{"msg":"hi"}`,
				Values: []string{"hi"},
			})
			m, _ = send(t, m, enter)
			require.True(t, m.showDetail)

			m, cmd := update(t, m, slash)
			require.Nil(t, cmd)
			require.True(t, m.showDetail)
			require.Equal(t, focusTable, m.focus)
			require.Nil(t, m.View().Cursor)
		})

		t.Run(
			"the page is two lines shorter than the window",
			func(t *testing.T) {
				m, asked := withBar(t, tea.WindowSizeMsg{Width: 40, Height: 10})

				require.Equal(t, 8, (*asked)[0].Limit)
				require.Equal(t, 8, m.paneHeight())
			},
		)

		t.Run("an error line shrinks the table by one row", func(t *testing.T) {
			m, asked := withBar(t, tea.WindowSizeMsg{Width: 40, Height: 10})

			m.bar = m.bar.SetError("unexpected }")
			m, cmd := m.layout()
			m, _ = run(t, m, cmd)

			require.Equal(t, 7, (*asked)[1].Limit)

			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			require.Len(t, lines, 10)
			require.Equal(t, "unexpected }", strings.TrimSpace(lines[1]))
			require.Contains(t, lines[9], "rows")
		})

		t.Run("the next edit clears the error and grows the table",
			func(t *testing.T) {
				m, asked := withBar(t, tea.WindowSizeMsg{Width: 40, Height: 10})
				m, _ = send(t, m, slash)
				m = typed(t, m, "ab")

				m.bar = m.bar.SetError("bad")
				m, cmd := m.layout()
				m, _ = run(t, m, cmd)
				n := len(*asked)

				// A cursor move keeps the error, and the table's size.
				m, _ = send(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
				require.Equal(t, 2, m.bar.Height())
				require.Len(t, *asked, n)

				m = typed(t, m, "x")

				require.Equal(t, 1, m.bar.Height())
				require.Equal(t, 8, lastQuery(asked).Limit)
			},
		)

		t.Run("Esc clears the error and grows the table", func(t *testing.T) {
			m, asked := withBar(t, tea.WindowSizeMsg{Width: 40, Height: 10})
			m, _ = send(t, m, slash)

			m.bar = m.bar.SetError("bad")
			m, cmd := m.layout()
			m, _ = run(t, m, cmd)

			_, _ = send(t, m, esc)

			require.Equal(t, 8, lastQuery(asked).Limit)
		})

		t.Run("an error resizes the detail pane too", func(t *testing.T) {
			m, _ := withRows(
				t,
				tea.WindowSizeMsg{Width: 40, Height: 10},
				logstore.Row{
					ID:     1,
					Raw:    `{"msg":"hi"}`,
					Values: []string{"hi"},
				},
			)
			m, _ = send(t, m, enter)

			// The query for the new height gets no answer.
			m.bar = m.bar.SetError("bad")
			m, _ = m.layout()

			// Bar, error, frame (7 lines) and status bar.
			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			require.Len(t, lines, 10)
			require.Equal(t, "╰", string([]rune(lines[8])[0]))
		})
	})

	t.Run("formatCount", func(t *testing.T) {
		for n, want := range map[int64]string{
			0: "0", 999: "999", 1000: "1,000", 12345: "12,345",
			1234567: "1,234,567",
		} {
			require.Equal(t, want, formatCount(n))
		}
	})

	t.Run("View", func(t *testing.T) {
		t.Run("uses the alternate screen", func(t *testing.T) {
			m := noRows(t, Options{})
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 40, Height: 3})

			v := m.View()
			require.True(t, v.AltScreen)
			require.Contains(t, v.Content, "0 rows")
		})

		t.Run("is empty before the window size is known", func(t *testing.T) {
			require.Empty(t, noRows(t, Options{}).View().Content)
		})
	})
}
