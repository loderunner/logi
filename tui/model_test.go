package tui

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
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

func TestModel(t *testing.T) {
	size := tea.WindowSizeMsg{Width: 40, Height: 6}

	t.Run("Update", func(t *testing.T) {
		t.Run(
			"Ingested returns a query that fills the table",
			func(t *testing.T) {
				ctrl := gomock.NewController(t)
				mockRows := tui_mock.NewMockRows(ctrl)
				// The status bar takes one of the 6 lines.
				mockRows.EXPECT().
					Newest(gomock.Any(), 5, []string{"_raw"}).
					Return([]logstore.Row{{ID: 2, Raw: "newer", Values: []string{""}}, {ID: 1, Raw: "older", Values: []string{""}}}, nil)

				m := New(mockRows, Options{FileSize: 100})
				m, cmd := update(t, m, size)
				require.NotNil(t, cmd)

				// The size query is superseded by the one from Ingested.
				m, cmd = update(t, m, ingest.Ingested{Rows: 2, Bytes: 10})
				require.NotNil(t, cmd)

				m, _ = update(t, m, cmd())

				require.Equal(t, "newer", m.table.Rows()[0].Raw)
				require.Contains(
					t,
					m.View().Content,
					"1970-01-01 00:00:00.000 newer\n1970-01-01 00:00:00.000 older",
				)
			},
		)

		t.Run("new keys change the keys the table queries", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRows := tui_mock.NewMockRows(ctrl)

			var asked [][]string

			mockRows.EXPECT().
				Newest(gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, _ int, keys []string) ([]logstore.Row, error) {
					asked = append(asked, keys)
					return nil, nil
				}).
				Times(4)

			m := New(mockRows, Options{FileSize: 100})
			m, cmd := update(t, m, size)
			cmd()

			// Keys that aren't level or message leave the default.
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
				{"_raw"}, {"_raw"}, {"Msg"}, {"LVL", "Msg"},
			}, asked)
			require.Equal(t, []string{"LVL", "Msg"}, m.keys)
			require.Equal(t, []string{"a", "Msg", "b", "LVL"}, m.known)
		})

		t.Run("drops a result queried with old keys", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRows := tui_mock.NewMockRows(ctrl)
			mockRows.EXPECT().
				Newest(gomock.Any(), gomock.Any(), []string{"_raw"}).
				Return([]logstore.Row{{ID: 1, Raw: "old", Values: []string{"x"}}}, nil)
			mockRows.EXPECT().
				Newest(gomock.Any(), gomock.Any(), []string{"msg"}).
				Return([]logstore.Row{{ID: 1, Raw: "new", Values: []string{"y"}}}, nil)

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
				Newest(gomock.Any(), gomock.Any(), gomock.Any()).
				Return([]logstore.Row{{ID: 1, Raw: "stale"}}, nil)
			mockRows.EXPECT().
				Newest(gomock.Any(), gomock.Any(), gomock.Any()).
				Return([]logstore.Row{{ID: 2, Raw: "fresh"}}, nil)

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
				Newest(gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, _ int, _ []string) ([]logstore.Row, error) {
					queryCtx = ctx
					return nil, nil
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
				Newest(gomock.Any(), gomock.Any(), gomock.Any()).
				Return(nil, errors.New("INTERRUPT Error: Interrupted!"))

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
					Newest(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, boom)

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

			require.Equal(t, "12,345 rows · 8,000 rows/s · 42%", m.status())
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

		t.Run("stdin shows reading, then EOF", func(t *testing.T) {
			m := New(mockRows, Options{Stdin: true})
			require.Equal(t, "reading", m.progress())

			m, _ = update(t, m, ingest.Ingested{Rows: 3, EOF: true})
			require.Equal(t, "EOF", m.progress())
			require.Equal(t, "3 rows · 0 rows/s · EOF", m.status())
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
