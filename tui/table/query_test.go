package table

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/loderunner/logi/ingest"
	"github.com/loderunner/logi/logstore"
	table_mock "github.com/loderunner/logi/tui/table/mock_"
)

// page is the height of the tables in these tests, and so the size of a page.
const page = 5

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

// press returns the press of the key with code.
func press(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}

func TestQueries(t *testing.T) {
	t.Run("Ingested", func(t *testing.T) {
		t.Run("returns a query that fills the table", func(t *testing.T) {
			mockRows := table_mock.NewMockRows(gomock.NewController(t))
			mockRows.EXPECT().
				Page(gomock.Any(), logstore.PageQuery{Limit: page, Keys: []string{"_raw"}}).
				Return(logstore.Page{Rows: rawRows("newer", "older")}, nil)

			m, cmd := New(mockRows).SetSize(40, page)
			require.NotNil(t, cmd)

			// The size query is superseded by the one from Ingested.
			m, cmd = m.Update(ingest.Ingested{Rows: 2})
			require.NotNil(t, cmd)

			m, _ = m.Update(cmd())

			require.Equal(t, stamp+"newer\n"+stamp+"older\n\n\n", shown(m))
		})

		t.Run("new keys change the keys the table queries", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			m, _ := New(mockRows).SetSize(40, page)

			// Until a level or message appears, keys show in order of
			// appearance.
			for _, newKeys := range [][]string{{"a"}, {"Msg", "b"}, {"LVL"}} {
				var cmd tea.Cmd

				m, cmd = m.Update(ingest.Ingested{NewKeys: newKeys})
				cmd()
			}

			var keys [][]string
			for _, q := range *asked {
				keys = append(keys, q.Keys)
			}

			// The Cmd's keys don't change when the model's do.
			require.Equal(t, [][]string{
				{"a"}, {"Msg", "a", "b"}, {"LVL", "Msg", "a", "b"},
			}, keys)
			require.Equal(t, []string{"a", "Msg", "b", "LVL"}, m.known)
		})

		t.Run("keeps the time key out of the columns", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			m, _ := New(mockRows).SetSize(40, page)
			_, cmd := m.Update(ingest.Ingested{
				NewKeys: []string{"time", "a", "msg", "level"},
				TimeKey: "time",
			})
			cmd()

			require.Equal(t, []string{"level", "msg", "a"}, (*asked)[0].Keys)
		})

		t.Run("shows the raw line when the time key is the only key",
			func(t *testing.T) {
				mockRows, asked := asks(t, logstore.Page{})

				m, _ := New(mockRows).SetSize(40, page)
				_, cmd := m.Update(ingest.Ingested{
					NewKeys: []string{"time"},
					TimeKey: "time",
				})
				cmd()

				require.Equal(t, []string{"_raw"}, (*asked)[0].Keys)
			},
		)

		t.Run("updates the columns when the time key arrives after the keys",
			func(t *testing.T) {
				mockRows, asked := asks(t, logstore.Page{})

				m, _ := New(mockRows).SetSize(40, page)

				// The first lines have no time, so the report has no time key
				// yet and the key still shows. A report that adds no key still
				// changes the columns, and later reports keep carrying the
				// time key.
				for _, msg := range []ingest.Ingested{
					{NewKeys: []string{"time", "msg"}},
					{TimeKey: "time"},
					{TimeKey: "time"},
				} {
					var cmd tea.Cmd

					m, cmd = m.Update(msg)
					cmd()
				}

				var keys [][]string
				for _, q := range *asked {
					keys = append(keys, q.Keys)
				}

				require.Equal(t, [][]string{
					{"msg", "time"}, {"msg"}, {"msg"},
				}, keys)
			},
		)

		t.Run("does not query before the size is known", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			_, cmd := New(mockRows).Update(ingest.Ingested{Rows: 1})

			require.Nil(t, cmd)
			require.Empty(t, *asked)
		})

		t.Run("while paused keeps from", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			m, _ := New(mockRows).SetSize(40, page)

			from := &logstore.Key{TS: time.Unix(0, 50), ID: 5}
			m.from = from

			m, cmd := m.Update(ingest.Ingested{Rows: 9})
			cmd()

			require.Equal(t, []logstore.PageQuery{{
				From:  from,
				Limit: page,
				Keys:  []string{"_raw"},
			}}, *asked)
			require.Same(t, from, m.from)
		})

		t.Run("keeps a move not yet confirmed", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			m, _ := New(mockRows).SetSize(40, page)
			m, _ = m.Update(press(tea.KeyPgDown))

			_, cmd := m.Update(ingest.Ingested{Rows: 9})
			cmd()

			require.Equal(t, page, (*asked)[0].Move)
		})
	})

	t.Run("PageMsg", func(t *testing.T) {
		t.Run("drops a page queried with old keys", func(t *testing.T) {
			mockRows := table_mock.NewMockRows(gomock.NewController(t))
			mockRows.EXPECT().
				Page(gomock.Any(), gomock.Cond(func(q logstore.PageQuery) bool {
					return q.Keys[0] == "_raw"
				})).
				Return(logstore.Page{Rows: cellRows([]string{"old"})}, nil)
			mockRows.EXPECT().
				Page(gomock.Any(), gomock.Cond(func(q logstore.PageQuery) bool {
					return q.Keys[0] == "msg"
				})).
				Return(logstore.Page{Rows: cellRows([]string{"new"})}, nil)

			m, oldCmd := New(mockRows).SetSize(40, page)
			m, newCmd := m.Update(ingest.Ingested{NewKeys: []string{"msg"}})

			m, _ = m.Update(newCmd())
			m, _ = m.Update(oldCmd())

			require.Equal(t, "new", m.rows[0].Values[0])
		})

		t.Run("drops a stale page", func(t *testing.T) {
			mockRows := table_mock.NewMockRows(gomock.NewController(t))
			mockRows.EXPECT().
				Page(gomock.Any(), gomock.Any()).
				Return(logstore.Page{Rows: rawRows("stale")}, nil)
			mockRows.EXPECT().
				Page(gomock.Any(), gomock.Any()).
				Return(logstore.Page{Rows: rawRows("fresh")}, nil)

			m, staleCmd := New(mockRows).SetSize(40, page)
			m, freshCmd := m.Update(ingest.Ingested{Rows: 1})

			stale := staleCmd()
			fresh := freshCmd()

			// The fresh page arrives first, then the stale one.
			m, _ = m.Update(fresh)
			m, _ = m.Update(stale)

			require.Equal(t, "fresh", m.rows[0].Raw)
		})
	})

	t.Run("a new query cancels the previous one", func(t *testing.T) {
		mockRows := table_mock.NewMockRows(gomock.NewController(t))

		var queryCtx context.Context

		mockRows.EXPECT().
			Page(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, _ logstore.PageQuery) (logstore.Page, error) {
				queryCtx = ctx
				return logstore.Page{}, nil
			})

		m, cmd := New(mockRows).SetSize(40, page)
		cmd()
		require.NoError(t, queryCtx.Err())

		_, _ = m.Update(ingest.Ingested{Rows: 1})
		require.ErrorIs(t, queryCtx.Err(), context.Canceled)
	})

	t.Run("a canceled query sends nothing", func(t *testing.T) {
		mockRows := table_mock.NewMockRows(gomock.NewController(t))
		mockRows.EXPECT().
			Page(gomock.Any(), gomock.Any()).
			Return(logstore.Page{}, errors.New("INTERRUPT Error: Interrupted!"))

		m, cmd := New(mockRows).SetSize(40, page)

		// The next query cancels the first one's context.
		_, _ = m.Update(ingest.Ingested{Rows: 1})

		require.Nil(t, cmd())
	})

	t.Run("Stop cancels the query in flight", func(t *testing.T) {
		mockRows := table_mock.NewMockRows(gomock.NewController(t))

		var queryCtx context.Context

		mockRows.EXPECT().
			Page(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, _ logstore.PageQuery) (logstore.Page, error) {
				queryCtx = ctx
				return logstore.Page{}, nil
			})

		m, cmd := New(mockRows).SetSize(40, page)
		m.Stop()

		require.Nil(t, cmd())
		require.ErrorIs(t, queryCtx.Err(), context.Canceled)
	})

	t.Run("a failed query reports its error", func(t *testing.T) {
		mockRows := table_mock.NewMockRows(gomock.NewController(t))
		boom := errors.New("boom")
		mockRows.EXPECT().
			Page(gomock.Any(), gomock.Any()).
			Return(logstore.Page{}, boom)

		_, cmd := New(mockRows).SetSize(40, page)

		msg, ok := cmd().(ErrMsg)
		require.True(t, ok)
		require.ErrorIs(t, msg.Err, boom)
	})

	t.Run("SetSize", func(t *testing.T) {
		t.Run("a new height keeps from", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			m, _ := New(mockRows).SetSize(40, page)

			from := &logstore.Key{ID: 5}
			m.from = from

			m, cmd := m.SetSize(40, 10)
			cmd()

			require.Equal(t, from, (*asked)[0].From)
			require.Equal(t, 10, (*asked)[0].Limit)

			// A page is the new height.
			_, cmd = m.Update(press(tea.KeyPgDown))
			cmd()

			require.Equal(t, 10, (*asked)[1].Move)
		})

		t.Run("a new width alone doesn't query", func(t *testing.T) {
			mockRows, _ := asks(t, logstore.Page{})

			m, _ := New(mockRows).SetSize(40, page)

			_, cmd := m.SetSize(60, page)
			require.Nil(t, cmd)
		})
	})

	t.Run("SetFilter", func(t *testing.T) {
		onlyErrors := logstore.Match{Key: "level", Value: "error"}

		t.Run("queries with the filter and keeps the view", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			m, _ := New(mockRows).SetSize(40, page)

			from := &logstore.Key{ID: 5}
			m.from = from

			_, cmd := m.SetFilter(onlyErrors)
			cmd()

			require.Equal(t, []logstore.PageQuery{{
				From:   from,
				Limit:  page,
				Keys:   []string{"_raw"},
				Filter: onlyErrors,
			}}, *asked)
		})

		t.Run("later queries keep the filter", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			m, _ := New(mockRows).SetSize(40, page)
			m, _ = m.SetFilter(onlyErrors)

			m, cmd := m.Update(ingest.Ingested{Rows: 1})
			cmd()
			require.Equal(t, onlyErrors, (*asked)[0].Filter)

			_, cmd = m.Update(press(tea.KeyDown))
			cmd()
			require.Equal(t, onlyErrors, (*asked)[1].Filter)
		})

		t.Run("nil removes the filter", func(t *testing.T) {
			mockRows, asked := asks(t, logstore.Page{})

			m, _ := New(mockRows).SetSize(40, page)
			m, _ = m.SetFilter(onlyErrors)

			_, cmd := m.SetFilter(nil)
			cmd()

			require.Nil(t, (*asked)[0].Filter)
		})

		t.Run(
			"drops a page from before the filter changed",
			func(t *testing.T) {
				mockRows := table_mock.NewMockRows(gomock.NewController(t))
				mockRows.EXPECT().
					Page(gomock.Any(), gomock.Any()).
					Return(logstore.Page{Rows: rawRows("all")}, nil)
				mockRows.EXPECT().
					Page(gomock.Any(), gomock.Any()).
					Return(logstore.Page{Rows: rawRows("errors")}, nil)

				m, unfilteredCmd := New(mockRows).SetSize(40, page)
				unfiltered := unfilteredCmd()

				m, filteredCmd := m.SetFilter(onlyErrors)

				// The page of the old query arrives after the filter changed.
				m, _ = m.Update(unfiltered)
				require.Empty(t, m.rows)

				m, _ = m.Update(filteredCmd())
				require.Equal(t, "errors", m.rows[0].Raw)
			},
		)
	})
}

func TestKeys(t *testing.T) {
	t.Run("each key moves the view", func(t *testing.T) {
		for name, tc := range map[string]struct {
			key  tea.KeyPressMsg
			move int
		}{
			"up":     {press(tea.KeyUp), -1},
			"down":   {press(tea.KeyDown), 1},
			"pgup":   {press(tea.KeyPgUp), -page},
			"pgdown": {press(tea.KeyPgDown), page},
		} {
			mockRows, asked := asks(t, logstore.Page{})

			m, _ := New(mockRows).SetSize(40, page)

			_, cmd := m.Update(tc.key)
			require.NotNil(t, cmd, name)
			cmd()

			require.Equal(t, []logstore.PageQuery{{
				Move:  tc.move,
				Limit: page,
				Keys:  []string{"_raw"},
			}}, *asked, name)
		}
	})

	t.Run("a page sets from and resets the move", func(t *testing.T) {
		from := &logstore.Key{TS: time.Unix(0, 50), ID: 5}
		mockRows, asked := asks(t, logstore.Page{
			From: from,
			Rows: rawRows("five"),
		})

		m, _ := New(mockRows).SetSize(40, page)
		require.True(t, m.Following())

		m, cmd := m.Update(press(tea.KeyDown))
		require.Equal(t, 1, m.move)

		m, _ = m.Update(cmd())
		require.Same(t, from, m.from)
		require.Zero(t, m.move)
		require.Equal(t, "five", m.rows[0].Raw)
		require.False(t, m.Following())

		// The next key starts from the confirmed row.
		m, cmd = m.Update(press(tea.KeyUp))
		cmd()

		require.Equal(t, logstore.PageQuery{
			From:  from,
			Move:  -1,
			Limit: page,
			Keys:  []string{"_raw"},
		}, (*asked)[1])
	})

	t.Run("a page that reaches the newest row follows again",
		func(t *testing.T) {
			mockRows, _ := asks(t, logstore.Page{})

			m, _ := New(mockRows).SetSize(40, page)
			m.from = &logstore.Key{ID: 5}
			require.False(t, m.Following())

			m, cmd := m.Update(press(tea.KeyPgUp))
			m, _ = m.Update(cmd())

			require.True(t, m.Following())
		})

	t.Run("two presses before a page send both", func(t *testing.T) {
		mockRows, asked := asks(t, logstore.Page{
			From: &logstore.Key{ID: 8},
		})

		m, _ := New(mockRows).SetSize(40, page)

		m, first := m.Update(press(tea.KeyDown))

		// The first query finishes before the second key press cancels it.
		stale := first()

		m, second := m.Update(press(tea.KeyDown))
		require.Equal(t, 2, m.move)

		fresh := second()

		require.Equal(t, 1, (*asked)[0].Move)
		require.Equal(t, 2, (*asked)[1].Move)

		// The first page is dropped by its generation number.
		m, _ = m.Update(stale)
		require.Equal(t, 2, m.move)
		require.Nil(t, m.from)

		m, _ = m.Update(fresh)
		require.Zero(t, m.move)
		require.Equal(t, &logstore.Key{ID: 8}, m.from)
	})

	t.Run("keys don't query before the size is known", func(t *testing.T) {
		mockRows, asked := asks(t, logstore.Page{})

		_, cmd := New(mockRows).Update(press(tea.KeyDown))

		require.Nil(t, cmd)
		require.Empty(t, *asked)
	})

	t.Run("Enter asks to open the top row", func(t *testing.T) {
		m := sized(40, page).setRows(rawRows("newest", "older"))

		_, cmd := m.Update(press(tea.KeyEnter))
		require.NotNil(t, cmd)

		msg, ok := cmd().(OpenMsg)
		require.True(t, ok)
		require.Equal(t, "newest", msg.Row.Raw)
	})

	t.Run("Enter on an empty table does nothing", func(t *testing.T) {
		_, cmd := sized(40, page).Update(press(tea.KeyEnter))

		require.Nil(t, cmd)
	})

	t.Run("other keys do nothing", func(t *testing.T) {
		m := sized(40, page).setRows(rawRows("a"))

		_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})

		require.Nil(t, cmd)
	})
}
