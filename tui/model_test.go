package tui

import (
	"context"
	"errors"
	"slices"
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
	tui_mock "github.com/loderunner/logi/tui/mock_"
)

// update applies msg and returns the resulting Model and command.
func update(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()

	next, cmd := m.Update(msg)

	model, ok := next.(Model)
	require.True(t, ok)

	return model, cmd
}

// keysAre matches a PageQuery that asks for keys.
func keysAre(keys ...string) gomock.Matcher {
	return gomock.Cond(func(q logstore.PageQuery) bool {
		return slices.Equal(q.Keys, keys)
	})
}

// withRows returns a model of the given size whose table shows rows, newest
// first. The mock serves a page of rows to the first query.
func withRows(
	t *testing.T,
	size tea.WindowSizeMsg,
	rows ...logstore.Row,
) (Model, *tui_mock.MockRows) {
	t.Helper()

	mockRows := tui_mock.NewMockRows(gomock.NewController(t))
	mockRows.EXPECT().
		Page(gomock.Any(), gomock.Any()).
		Return(logstore.Page{Rows: rows}, nil)

	m := New(mockRows, Options{FileSize: 100})
	m, cmd := update(t, m, size)
	m, _ = update(t, m, cmd())

	return m, mockRows
}

func TestModel(t *testing.T) {
	// The bar and the status bar take two of the 7 lines, which leaves a table
	// 5 rows high.
	size := tea.WindowSizeMsg{Width: 40, Height: 7}

	t.Run("Update", func(t *testing.T) {
		t.Run(
			"Ingested returns a query that fills the table",
			func(t *testing.T) {
				ctrl := gomock.NewController(t)
				mockRows := tui_mock.NewMockRows(ctrl)
				mockRows.EXPECT().
					Page(gomock.Any(), logstore.PageQuery{Limit: 5, Keys: []string{"_raw"}}).
					Return(logstore.Page{Rows: []logstore.Row{{ID: 2, Raw: "newer", Values: []string{""}}, {ID: 1, Raw: "older", Values: []string{""}}}}, nil)

				m := New(mockRows, Options{FileSize: 100})
				m, cmd := update(t, m, size)
				require.NotNil(t, cmd)

				// The size query is superseded by the one from Ingested.
				m, cmd = update(t, m, ingest.Ingested{Rows: 2, Bytes: 10})
				require.NotNil(t, cmd)

				m, _ = update(t, m, cmd())

				require.Equal(t, "newer", m.table.Rows()[0].Raw)
				view := ansi.Strip(m.View().Content)

				// The top row is padded to the width, so it ends in spaces.
				require.Contains(t, view, "0001-01-01 00:00:00.000 newer ")
				require.Contains(t, view, "\n0001-01-01 00:00:00.000 older\n")
			},
		)

		t.Run("new keys change the keys the table queries", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRows := tui_mock.NewMockRows(ctrl)

			var asked [][]string

			mockRows.EXPECT().
				Page(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, q logstore.PageQuery) (logstore.Page, error) {
					asked = append(asked, q.Keys)
					return logstore.Page{}, nil
				}).
				Times(4)

			m := New(mockRows, Options{FileSize: 100})
			m, cmd := update(t, m, size)
			cmd()

			// Until a level or message appears, keys show in order of appearance.
			m, cmd = update(
				t,
				m,
				ingest.Ingested{Rows: 1, NewKeys: []string{"a"}},
			)
			cmd()

			m, cmd = update(
				t,
				m,
				ingest.Ingested{Rows: 2, NewKeys: []string{"Msg", "b"}},
			)
			cmd()

			m, cmd = update(
				t,
				m,
				ingest.Ingested{Rows: 3, NewKeys: []string{"LVL"}},
			)
			cmd()

			// The Cmd's keys don't change when the model's do.
			require.Equal(t, [][]string{
				{"_raw"}, {"a"}, {"Msg", "a", "b"}, {"LVL", "Msg", "a", "b"},
			}, asked)
			require.Equal(t, []string{"LVL", "Msg", "a", "b"}, m.keys)
			require.Equal(t, []string{"a", "Msg", "b", "LVL"}, m.known)
		})

		t.Run(
			"keeps the time key out of the default columns",
			func(t *testing.T) {
				ctrl := gomock.NewController(t)
				mockRows := tui_mock.NewMockRows(ctrl)
				mockRows.EXPECT().
					Page(gomock.Any(), keysAre("level", "msg", "a")).
					Return(logstore.Page{}, nil)

				m := New(mockRows, Options{FileSize: 100})
				m, _ = update(t, m, size)
				m, cmd := update(t, m, ingest.Ingested{
					Rows:    1,
					NewKeys: []string{"time", "a", "msg", "level"},
					TimeKey: "time",
				})
				cmd()

				require.Equal(t, []string{"level", "msg", "a"}, m.keys)
				require.Equal(t, []string{"time", "a", "msg", "level"}, m.known)
			},
		)

		t.Run("shows the raw line when the time key is the only key",
			func(t *testing.T) {
				ctrl := gomock.NewController(t)
				mockRows := tui_mock.NewMockRows(ctrl)
				mockRows.EXPECT().
					Page(gomock.Any(), keysAre("_raw")).
					Return(logstore.Page{}, nil)

				m := New(mockRows, Options{FileSize: 100})
				m, _ = update(t, m, size)
				m, cmd := update(t, m, ingest.Ingested{
					Rows:    1,
					NewKeys: []string{"time"},
					TimeKey: "time",
				})
				cmd()

				require.Equal(t, []string{"_raw"}, m.keys)
			},
		)

		t.Run("updates the columns when the time key arrives after the keys",
			func(t *testing.T) {
				ctrl := gomock.NewController(t)
				mockRows := tui_mock.NewMockRows(ctrl)

				var asked [][]string

				mockRows.EXPECT().
					Page(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, q logstore.PageQuery) (logstore.Page, error) {
						asked = append(asked, q.Keys)
						return logstore.Page{}, nil
					}).
					Times(3)

				m := New(mockRows, Options{FileSize: 100})
				m, _ = update(t, m, size)

				// The first lines have no time, so the report has no time key
				// yet and the key still shows.
				m, cmd := update(t, m, ingest.Ingested{
					Rows:    1,
					NewKeys: []string{"time", "msg"},
				})
				cmd()
				require.Equal(t, []string{"msg", "time"}, m.keys)

				// A report that adds no key still changes the columns.
				m, cmd = update(t, m, ingest.Ingested{
					Rows:    2,
					TimeKey: "time",
				})
				cmd()
				require.Equal(t, []string{"msg"}, m.keys)

				// Later reports keep carrying the time key.
				m, cmd = update(t, m, ingest.Ingested{
					Rows:    3,
					TimeKey: "time",
				})
				cmd()

				require.Equal(t, []string{"msg"}, m.keys)
				require.Equal(t, [][]string{
					{"msg", "time"}, {"msg"}, {"msg"},
				}, asked)
			},
		)

		t.Run("drops a result queried with old keys", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRows := tui_mock.NewMockRows(ctrl)
			mockRows.EXPECT().
				Page(gomock.Any(), keysAre("_raw")).
				Return(logstore.Page{Rows: []logstore.Row{{ID: 1, Raw: "old", Values: []string{"x"}}}}, nil)
			mockRows.EXPECT().
				Page(gomock.Any(), keysAre("msg")).
				Return(logstore.Page{Rows: []logstore.Row{{ID: 1, Raw: "new", Values: []string{"y"}}}}, nil)

			m := New(mockRows, Options{FileSize: 100})
			m, oldCmd := update(t, m, size)
			m, newCmd := update(
				t,
				m,
				ingest.Ingested{Rows: 1, NewKeys: []string{"msg"}},
			)

			m, _ = update(t, m, newCmd())
			m, _ = update(t, m, oldCmd())

			require.Equal(t, "new", m.table.Rows()[0].Raw)
		})

		t.Run("drops a stale result", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRows := tui_mock.NewMockRows(ctrl)
			mockRows.EXPECT().
				Page(gomock.Any(), gomock.Any()).
				Return(logstore.Page{Rows: []logstore.Row{{ID: 1, Raw: "stale"}}}, nil)
			mockRows.EXPECT().
				Page(gomock.Any(), gomock.Any()).
				Return(logstore.Page{Rows: []logstore.Row{{ID: 2, Raw: "fresh"}}}, nil)

			m := New(mockRows, Options{FileSize: 100})
			m, staleCmd := update(t, m, size)
			m, freshCmd := update(t, m, ingest.Ingested{Rows: 1})

			stale := staleCmd()
			fresh := freshCmd()

			// The fresh result arrives first, then the stale one.
			m, _ = update(t, m, fresh)
			m, _ = update(t, m, stale)

			require.Equal(t, "fresh", m.table.Rows()[0].Raw)
		})

		t.Run("cancels the previous query", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRows := tui_mock.NewMockRows(ctrl)

			var queryCtx context.Context

			mockRows.EXPECT().
				Page(gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, _ logstore.PageQuery) (logstore.Page, error) {
					queryCtx = ctx
					return logstore.Page{}, nil
				})

			m := New(mockRows, Options{FileSize: 100})
			m, cmd := update(t, m, size)
			cmd()
			require.NoError(t, queryCtx.Err())

			_, _ = update(t, m, ingest.Ingested{Rows: 1})
			require.ErrorIs(t, queryCtx.Err(), context.Canceled)
		})

		t.Run("a canceled query sends nothing", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRows := tui_mock.NewMockRows(ctrl)
			mockRows.EXPECT().
				Page(gomock.Any(), gomock.Any()).
				Return(logstore.Page{}, errors.New("INTERRUPT Error: Interrupted!"))

			m := New(mockRows, Options{FileSize: 100})
			m, cmd := update(t, m, size)

			// The next refresh cancels the first query's context.
			_, _ = update(t, m, ingest.Ingested{Rows: 1})

			msg := cmd()
			require.Nil(t, msg)
		})

		t.Run(
			"does not query before the window size is known",
			func(t *testing.T) {
				ctrl := gomock.NewController(t)
				mockRows := tui_mock.NewMockRows(ctrl)

				m := New(mockRows, Options{FileSize: 100})
				_, cmd := update(t, m, ingest.Ingested{Rows: 1})

				require.Nil(t, cmd)
			},
		)

		t.Run(
			"a failed query ends the program with its error",
			func(t *testing.T) {
				ctrl := gomock.NewController(t)
				mockRows := tui_mock.NewMockRows(ctrl)
				boom := errors.New("boom")
				mockRows.EXPECT().
					Page(gomock.Any(), gomock.Any()).
					Return(logstore.Page{}, boom)

				m := New(mockRows, Options{FileSize: 100})
				m, cmd := update(t, m, size)

				m, cmd = update(t, m, cmd())
				require.ErrorIs(t, m.Err(), boom)
				require.NotNil(t, cmd)

				_, ok := cmd().(tea.QuitMsg)
				require.True(t, ok)
			},
		)

		t.Run("an ingest error ends the program", func(t *testing.T) {
			m := New(
				tui_mock.NewMockRows(gomock.NewController(t)),
				Options{},
			)
			boom := errors.New("boom")

			m, cmd := update(t, m, ErrMsg{Err: boom})
			require.ErrorIs(t, m.Err(), boom)

			_, ok := cmd().(tea.QuitMsg)
			require.True(t, ok)
		})

		t.Run("q and ctrl+c quit", func(t *testing.T) {
			for _, key := range []tea.KeyPressMsg{
				{Code: 'q', Text: "q"},
				{Code: 'c', Mod: tea.ModCtrl},
			} {
				m := New(
					tui_mock.NewMockRows(gomock.NewController(t)),
					Options{},
				)

				_, cmd := update(t, m, key)
				require.NotNil(t, cmd, key.String())

				_, ok := cmd().(tea.QuitMsg)
				require.True(t, ok, key.String())
			}
		})

		t.Run("detail pane", func(t *testing.T) {
			enter := tea.KeyPressMsg{Code: tea.KeyEnter}
			esc := tea.KeyPressMsg{Code: tea.KeyEscape}

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

			// firstLine returns the first line of text in the detail
			// pane, inside its frame, under the bar and the frame's top.
			firstLine := func(m Model) string {
				lines := strings.Split(ansi.Strip(m.View().Content), "\n")
				line := strings.TrimSuffix(lines[2], "│")

				return strings.TrimRight(strings.TrimPrefix(line, "│ "), " ")
			}

			t.Run("Enter opens the top row's JSON", func(t *testing.T) {
				m, _ := withRows(t, size, newest, older)

				m, cmd := update(t, m, enter)
				require.Nil(t, cmd)
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
				m, _ = update(t, m, enter)

				m, cmd := update(t, m, esc)
				require.Nil(t, cmd)
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

					m, _ = update(t, m, enter)

					require.Equal(t, "not json    �[31mred", firstLine(m))
				},
			)

			t.Run("Enter on an empty table does nothing", func(t *testing.T) {
				m, _ := withRows(t, size)

				m, cmd := update(t, m, enter)
				require.Nil(t, cmd)
				require.False(t, m.showDetail)
			})

			t.Run("Enter before the window size is known does nothing",
				func(t *testing.T) {
					m := New(
						tui_mock.NewMockRows(gomock.NewController(t)),
						Options{},
					)

					m, cmd := update(t, m, enter)
					require.Nil(t, cmd)
					require.False(t, m.showDetail)
				},
			)

			t.Run(
				"Esc with the table showing does nothing",
				func(t *testing.T) {
					m, _ := withRows(t, size, newest)

					m, cmd := update(t, m, esc)
					require.Nil(t, cmd)
					require.False(t, m.showDetail)
				},
			)

			t.Run("the arrow and page keys scroll", func(t *testing.T) {
				// The pane shows 3 lines inside its frame, and the mock
				// expects one Page call, for the first query, so scrolling
				// sends no query.
				m, _ := withRows(t, size, logstore.Row{
					ID:     1,
					Raw:    `{"a":[1,2,3,4,5,6,7,8]}`,
					Values: []string{""},
				})
				m, _ = update(t, m, enter)

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
					var cmd tea.Cmd

					m, cmd = update(t, m, tea.KeyPressMsg{Code: step.key})
					require.Nil(t, cmd)
					require.True(t, m.showDetail)
					require.Zero(t, m.move)
					require.Equal(t, step.want, firstLine(m))
				}
			})

			t.Run("q and ctrl+c quit", func(t *testing.T) {
				for _, key := range []tea.KeyPressMsg{
					{Code: 'q', Text: "q"},
					{Code: 'c', Mod: tea.ModCtrl},
				} {
					m, _ := withRows(t, size, newest)
					m, _ = update(t, m, enter)

					_, cmd := update(t, m, key)
					require.NotNil(t, cmd, key.String())

					_, ok := cmd().(tea.QuitMsg)
					require.True(t, ok, key.String())
				}
			})

			t.Run("Ingested still queries a page and updates the table",
				func(t *testing.T) {
					m, mockRows := withRows(t, size, newest, older)
					m, _ = update(t, m, enter)

					fresh := logstore.Row{
						ID:     3,
						Raw:    `{"msg":"fresh"}`,
						Values: []string{"fresh"},
					}
					mockRows.EXPECT().
						Page(gomock.Any(), gomock.Any()).
						Return(logstore.Page{Rows: []logstore.Row{fresh, newest}}, nil)

					m, cmd := update(t, m, ingest.Ingested{Rows: 3})
					require.NotNil(t, cmd)

					m, _ = update(t, m, cmd())
					require.Equal(t, "fresh", m.table.Rows()[0].Values[0])

					// The pane still shows the row it opened on.
					require.Contains(t, ansi.Strip(m.View().Content), `"hi"`)

					m, _ = update(t, m, esc)
					require.Contains(
						t,
						ansi.Strip(m.View().Content),
						"0001-01-01 00:00:00.000 fresh",
					)
				},
			)

			t.Run("a resize resizes the pane", func(t *testing.T) {
				m, _ := withRows(t, size, newest)
				m, _ = update(t, m, enter)

				m, _ = update(t, m, tea.WindowSizeMsg{Width: 8, Height: 7})

				require.Contains(
					t,
					ansi.Strip(m.View().Content),
					"  \"msg\":\n",
				)
			})
		})

		t.Run("other keys do nothing", func(t *testing.T) {
			m := New(
				tui_mock.NewMockRows(gomock.NewController(t)),
				Options{},
			)

			_, cmd := update(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
			require.Nil(t, cmd)
		})
	})

	t.Run("rows per second", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			m := New(tui_mock.NewMockRows(gomock.NewController(t)), Options{})

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
		ctrl := gomock.NewController(t)
		mockRows := tui_mock.NewMockRows(ctrl)

		t.Run("a file shows the percentage loaded", func(t *testing.T) {
			m := New(mockRows, Options{FileSize: 200})
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
			m := New(mockRows, Options{FileSize: 100})
			m.bytes = 250

			require.Equal(t, "100%", m.progress())
		})

		t.Run("an empty file is fully loaded", func(t *testing.T) {
			m := New(mockRows, Options{})

			require.Equal(t, "100%", m.progress())
		})

		t.Run(
			"shows following until the view scrolls away",
			func(t *testing.T) {
				m := New(mockRows, Options{Stdin: true})
				require.Equal(
					t,
					"0 rows · 0 rows/s · reading · following",
					m.status(),
				)

				m.from = &logstore.Key{ID: 5}
				require.Equal(
					t,
					"0 rows · 0 rows/s · reading · paused",
					m.status(),
				)
			},
		)

		t.Run("stdin shows reading, then EOF", func(t *testing.T) {
			m := New(mockRows, Options{Stdin: true})
			require.Equal(t, "reading", m.progress())

			m, _ = update(t, m, ingest.Ingested{Rows: 3, EOF: true})
			require.Equal(t, "EOF", m.progress())
			require.Equal(t, "3 rows · 0 rows/s · EOF · following", m.status())
		})
	})

	t.Run("filter bar", func(t *testing.T) {
		slash := tea.KeyPressMsg{Code: '/', Text: "/"}
		enter := tea.KeyPressMsg{Code: tea.KeyEnter}
		esc := tea.KeyPressMsg{Code: tea.KeyEscape}
		ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

		// typed returns m after the keys that type text.
		typed := func(t *testing.T, m Model, text string) Model {
			t.Helper()

			for _, r := range text {
				m, _ = update(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
			}

			return m
		}

		// quits reports whether cmd quits the program.
		quits := func(cmd tea.Cmd) bool {
			if cmd == nil {
				return false
			}

			_, ok := cmd().(tea.QuitMsg)

			return ok
		}

		// withBar returns a model of the given size with a mock that
		// records every page query, and the table showing no rows.
		withBar := func(
			t *testing.T,
			size tea.WindowSizeMsg,
		) (Model, *[]logstore.PageQuery) {
			t.Helper()

			mockRows := tui_mock.NewMockRows(gomock.NewController(t))

			var asked []logstore.PageQuery

			mockRows.EXPECT().
				Page(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, q logstore.PageQuery) (logstore.Page, error) {
					asked = append(asked, q)
					return logstore.Page{}, nil
				}).
				AnyTimes()

			m := New(mockRows, Options{FileSize: 100})
			m, cmd := update(t, m, size)
			cmd()

			return m, &asked
		}

		t.Run("the table has focus at startup", func(t *testing.T) {
			m, _ := withRows(t, size)

			require.Equal(t, focusTable, m.focus)
			require.False(t, m.bar.Focused())
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
			require.True(t, m.bar.Focused())

			// The bar's cursor is on its row, after the prompt.
			cursor := m.View().Cursor
			require.NotNil(t, cursor)
			require.Equal(t, 2, cursor.X)
			require.Equal(t, 0, cursor.Y)

			// The slash itself isn't typed.
			require.Empty(t, m.bar.Value())
		})

		t.Run("shows no cursor while the table has focus", func(t *testing.T) {
			m, _ := withRows(t, size)

			require.Nil(t, m.View().Cursor)
		})

		t.Run("types q and the other printable keys", func(t *testing.T) {
			m, _ := withRows(t, size)
			m, _ = update(t, m, slash)

			m = typed(t, m, "level:error q /+-=c")

			require.Equal(t, "level:error q /+-=c", m.bar.Value())
			require.Equal(t, focusFilter, m.focus)
		})

		t.Run("Enter keeps the text and returns focus to the table",
			func(t *testing.T) {
				m, _ := withRows(t, size)
				m, _ = update(t, m, slash)
				m = typed(t, m, "level:error")

				m, cmd := update(t, m, enter)
				require.Nil(t, cmd)
				require.Equal(t, focusTable, m.focus)
				require.False(t, m.bar.Focused())
				require.Equal(t, "level:error", m.bar.Value())
				require.Nil(t, m.View().Cursor)
				require.Contains(
					t,
					ansi.Strip(m.View().Content),
					"/ level:error",
				)

				// The table takes the keys again, so q quits.
				_, cmd = update(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})
				require.True(t, quits(cmd))
			},
		)

		t.Run("Enter doesn't query", func(t *testing.T) {
			m, asked := withBar(t, size)
			require.Len(t, *asked, 1)

			m, _ = update(t, m, slash)
			m = typed(t, m, "level:error")
			_, cmd := update(t, m, enter)

			require.Nil(t, cmd)
			require.Len(t, *asked, 1)
		})

		t.Run("Esc reverts to the committed text", func(t *testing.T) {
			m, _ := withRows(t, size)
			m, _ = update(t, m, slash)
			m = typed(t, m, "level:error")
			m, _ = update(t, m, enter)

			m, _ = update(t, m, slash)
			m = typed(t, m, " x")
			require.Equal(t, "level:error x", m.bar.Value())

			m, cmd := update(t, m, esc)
			require.Nil(t, cmd)
			require.Equal(t, focusTable, m.focus)
			require.False(t, m.bar.Focused())
			require.Equal(t, "level:error", m.bar.Value())
		})

		t.Run("Esc before any commit empties the bar", func(t *testing.T) {
			m, _ := withRows(t, size)
			m, _ = update(t, m, slash)
			m = typed(t, m, "abc")

			m, _ = update(t, m, esc)
			require.Equal(t, focusTable, m.focus)
			require.Empty(t, m.bar.Value())
		})

		t.Run("ctrl+c quits from the bar", func(t *testing.T) {
			m, _ := withRows(t, size)
			m, _ = update(t, m, slash)
			m = typed(t, m, "abc")

			_, cmd := update(t, m, ctrlC)
			require.True(t, quits(cmd))
		})

		t.Run("q quits from the table, not from the bar", func(t *testing.T) {
			m, _ := withRows(t, size)
			q := tea.KeyPressMsg{Code: 'q', Text: "q"}

			_, cmd := update(t, m, q)
			require.True(t, quits(cmd))

			m, _ = update(t, m, slash)
			_, cmd = update(t, m, q)
			require.False(t, quits(cmd))
		})

		t.Run("the table's keys go to the bar while it has focus",
			func(t *testing.T) {
				m, asked := withBar(t, size)
				m, _ = update(t, m, slash)

				for _, k := range []rune{tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown} {
					m, _ = update(t, m, tea.KeyPressMsg{Code: k})
				}

				require.Len(t, *asked, 1)
			},
		)

		t.Run("a paste goes to the bar", func(t *testing.T) {
			m, _ := withRows(t, size)
			m, _ = update(t, m, slash)

			m, _ = update(t, m, tea.PasteMsg{Content: "level:error"})
			require.Equal(t, "level:error", m.bar.Value())
		})

		t.Run(
			"a paste is ignored while the table has focus",
			func(t *testing.T) {
				m, _ := withRows(t, size)

				m, _ = update(t, m, tea.PasteMsg{Content: "level:error"})
				require.Empty(t, m.bar.Value())
			},
		)

		t.Run("/ does nothing in the detail pane", func(t *testing.T) {
			m, _ := withRows(t, size, logstore.Row{
				ID:     1,
				Raw:    `{"msg":"hi"}`,
				Values: []string{"hi"},
			})
			m, _ = update(t, m, enter)
			require.True(t, m.showDetail)

			m, cmd := update(t, m, slash)
			require.Nil(t, cmd)
			require.True(t, m.showDetail)
			require.Equal(t, focusTable, m.focus)
			require.False(t, m.bar.Focused())
			require.Nil(t, m.View().Cursor)
		})

		t.Run("the detail pane still closes on Esc and quits on q",
			func(t *testing.T) {
				m, _ := withRows(t, size, logstore.Row{
					ID:     1,
					Raw:    `{"msg":"hi"}`,
					Values: []string{"hi"},
				})
				m, _ = update(t, m, enter)

				_, cmd := update(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})
				require.True(t, quits(cmd))

				m, _ = update(t, m, esc)
				require.False(t, m.showDetail)
			},
		)

		t.Run(
			"the page is two lines shorter than the window",
			func(t *testing.T) {
				m, asked := withBar(t, tea.WindowSizeMsg{Width: 40, Height: 10})

				require.Equal(t, 8, (*asked)[0].Limit)
				require.Equal(t, 8, m.tableHeight())
			},
		)

		t.Run("an error line shrinks the table by one row", func(t *testing.T) {
			m, asked := withBar(t, tea.WindowSizeMsg{Width: 40, Height: 10})

			m.bar = m.bar.SetError("unexpected }")
			m, cmd := m.layout()
			require.NotNil(t, cmd)
			cmd()

			require.Equal(t, 7, (*asked)[1].Limit)

			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			require.Len(t, lines, 10)
			require.Equal(t, "unexpected }", strings.TrimSpace(lines[1]))
			require.Contains(t, lines[9], "rows")
		})

		t.Run("the next edit clears the error and grows the table",
			func(t *testing.T) {
				m, asked := withBar(t, tea.WindowSizeMsg{Width: 40, Height: 10})
				m, _ = update(t, m, slash)
				m = typed(t, m, "ab")

				m.bar = m.bar.SetError("bad")
				m, cmd := m.layout()
				cmd()

				// A cursor move keeps the error, and the table's size.
				m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
				require.Nil(t, cmd)
				require.Equal(t, 2, m.bar.Height())

				m, cmd = update(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
				require.NotNil(t, cmd)
				cmd()

				require.Equal(t, 1, m.bar.Height())
				require.Equal(t, 8, (*asked)[len(*asked)-1].Limit)
			},
		)

		t.Run("Esc clears the error and grows the table", func(t *testing.T) {
			m, asked := withBar(t, tea.WindowSizeMsg{Width: 40, Height: 10})
			m, _ = update(t, m, slash)

			m.bar = m.bar.SetError("bad")
			m, cmd := m.layout()
			cmd()

			m, cmd = update(t, m, esc)
			require.NotNil(t, cmd)
			cmd()

			require.Equal(t, 8, (*asked)[len(*asked)-1].Limit)
		})

		t.Run("an error resizes the detail pane too", func(t *testing.T) {
			m, _ := withBar(t, tea.WindowSizeMsg{Width: 40, Height: 10})
			m.table = m.table.SetRows([]logstore.Row{
				{ID: 1, Raw: `{"msg":"hi"}`, Values: []string{"hi"}},
			})
			m, _ = update(t, m, enter)

			m.bar = m.bar.SetError("bad")
			m, _ = m.layout()

			// Bar, error, frame (7 lines) and status bar.
			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			require.Len(t, lines, 10)
			require.Equal(t, "╰", string([]rune(lines[8])[0]))
		})
	})

	t.Run("scrolling", func(t *testing.T) {
		// A table 5 rows high: the bar and the status bar take two of the 7 lines.
		const page = 5

		// asks records the query of every Page call and answers with answer.
		asks := func(
			t *testing.T,
			answer logstore.Page,
		) (*tui_mock.MockRows, *[]logstore.PageQuery) {
			t.Helper()

			ctrl := gomock.NewController(t)
			mockRows := tui_mock.NewMockRows(ctrl)

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

		key := func(code rune) tea.KeyPressMsg {
			return tea.KeyPressMsg{Code: code}
		}

		t.Run("each key moves the view", func(t *testing.T) {
			for name, tc := range map[string]struct {
				key  tea.KeyPressMsg
				move int
			}{
				"up":     {key(tea.KeyUp), -1},
				"down":   {key(tea.KeyDown), 1},
				"pgup":   {key(tea.KeyPgUp), -page},
				"pgdown": {key(tea.KeyPgDown), page},
			} {
				mockRows, asked := asks(t, logstore.Page{})

				m := New(mockRows, Options{FileSize: 100})
				m, _ = update(t, m, size)

				_, cmd := update(t, m, tc.key)
				require.NotNil(t, cmd, name)
				cmd()

				require.Equal(t, []logstore.PageQuery{{
					Move:  tc.move,
					Limit: page,
					Keys:  []string{"_raw"},
				}}, *asked, name)
			}
		})

		t.Run("a result sets from and resets the move", func(t *testing.T) {
			from := &logstore.Key{TS: time.Unix(0, 50), ID: 5}
			mockRows, asked := asks(t, logstore.Page{
				From: from,
				Rows: []logstore.Row{
					{ID: 5, Raw: "five", Values: []string{""}},
				},
			})

			m := New(mockRows, Options{FileSize: 100})
			m, _ = update(t, m, size)
			require.Contains(t, m.status(), "· following")

			m, cmd := update(t, m, key(tea.KeyDown))
			require.Equal(t, 1, m.move)

			m, _ = update(t, m, cmd())
			require.Same(t, from, m.from)
			require.Zero(t, m.move)
			require.Equal(t, "five", m.table.Rows()[0].Raw)
			require.Contains(t, m.status(), "· paused")

			// The next key starts from the confirmed row.
			m, cmd = update(t, m, key(tea.KeyUp))
			cmd()

			require.Equal(t, logstore.PageQuery{
				From:  from,
				Move:  -1,
				Limit: page,
				Keys:  []string{"_raw"},
			}, (*asked)[1])
		})

		t.Run("a result that reaches the newest row follows again",
			func(t *testing.T) {
				mockRows, _ := asks(t, logstore.Page{})

				m := New(mockRows, Options{FileSize: 100})
				m, _ = update(t, m, size)
				m.from = &logstore.Key{ID: 5}
				require.Contains(t, m.status(), "paused")

				m, cmd := update(t, m, key(tea.KeyPgUp))
				m, _ = update(t, m, cmd())

				require.Nil(t, m.from)
				require.Contains(t, m.status(), "following")
			})

		t.Run("two presses before a result send both", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{
				From: &logstore.Key{ID: 8},
			})

			m := New(mockRows, Options{FileSize: 100})
			m, _ = update(t, m, size)

			m, first := update(t, m, key(tea.KeyDown))

			// The first query finishes before the second key press cancels it.
			stale := first()

			m, second := update(t, m, key(tea.KeyDown))
			require.Equal(t, 2, m.move)

			fresh := second()

			require.Equal(t, 1, (*asked)[0].Move)
			require.Equal(t, 2, (*asked)[1].Move)

			// The first result is dropped by its generation number.
			m, _ = update(t, m, stale)
			require.Equal(t, 2, m.move)
			require.Nil(t, m.from)

			m, _ = update(t, m, fresh)
			require.Zero(t, m.move)
			require.Equal(t, &logstore.Key{ID: 8}, m.from)
		})

		t.Run("Ingested while paused keeps from", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			m := New(mockRows, Options{FileSize: 100})
			m, _ = update(t, m, size)

			from := &logstore.Key{TS: time.Unix(0, 50), ID: 5}
			m.from = from

			m, cmd := update(t, m, ingest.Ingested{Rows: 9})
			cmd()

			require.Equal(t, []logstore.PageQuery{{
				From:  from,
				Limit: page,
				Keys:  []string{"_raw"},
			}}, *asked)
			require.Same(t, from, m.from)
		})

		t.Run("Ingested keeps a move not yet confirmed", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			m := New(mockRows, Options{FileSize: 100})
			m, _ = update(t, m, size)

			m, _ = update(t, m, key(tea.KeyPgDown))
			_, cmd := update(t, m, ingest.Ingested{Rows: 9})
			cmd()

			require.Equal(t, page, (*asked)[0].Move)
		})

		t.Run("resizing keeps from", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			m := New(mockRows, Options{FileSize: 100})
			m, _ = update(t, m, size)

			from := &logstore.Key{ID: 5}
			m.from = from

			m, cmd := update(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
			cmd()

			require.Equal(t, from, (*asked)[0].From)
			require.Equal(t, 10, (*asked)[0].Limit)

			// A page is the new height.
			m, cmd = update(t, m, key(tea.KeyPgDown))
			cmd()

			require.Equal(t, 10, (*asked)[1].Move)
		})

		t.Run("keys do nothing before the window size is known",
			func(t *testing.T) {
				mockRows, asked := asks(t, logstore.Page{})

				m := New(mockRows, Options{FileSize: 100})
				_, cmd := update(t, m, key(tea.KeyDown))

				require.Nil(t, cmd)
				require.Empty(t, *asked)
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
			m := New(
				tui_mock.NewMockRows(gomock.NewController(t)),
				Options{},
			)
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 40, Height: 3})

			v := m.View()
			require.True(t, v.AltScreen)
			require.Contains(t, v.Content, "0 rows")
		})

		t.Run("is empty before the window size is known", func(t *testing.T) {
			m := New(
				tui_mock.NewMockRows(gomock.NewController(t)),
				Options{},
			)

			require.Empty(t, m.View().Content)
		})
	})
}
