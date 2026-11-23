package table

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/loderunner/logi/logstore"
)

// stamp is the timestamp column of a row with TS 0.
const stamp = "1970-01-01 00:00:00.000 "

// rawRows returns rows with no cell, so that each shows its raw line.
func rawRows(raws ...string) []logstore.Row {
	rows := make([]logstore.Row, len(raws))
	for i, raw := range raws {
		rows[i] = logstore.Row{ID: int64(i), Raw: raw, Values: []string{""}}
	}

	return rows
}

// cellRows returns rows with the given cells, one slice per row.
func cellRows(cells ...[]string) []logstore.Row {
	rows := make([]logstore.Row, len(cells))
	for i, values := range cells {
		rows[i] = logstore.Row{ID: int64(i), Raw: "raw", Values: values}
	}

	return rows
}

func TestModel(t *testing.T) {
	t.Run("View", func(t *testing.T) {
		t.Run(
			"shows one line per row, padded to the height",
			func(t *testing.T) {
				m := New().SetSize(60, 4).SetRows(rawRows("newest", "older"))

				require.Equal(
					t,
					stamp+"newest\n"+stamp+"older\n\n",
					m.View(),
				)
			},
		)

		t.Run(
			"formats the timestamp in UTC with milliseconds",
			func(t *testing.T) {
				ts := time.Date(2026, 3, 4, 5, 6, 7, 89_000_000, time.UTC).
					UnixNano()
				rows := rawRows("x")
				rows[0].TS = ts

				m := New().SetSize(60, 1).SetRows(rows)

				require.Equal(t, "2026-03-04 05:06:07.089 x", m.View())
			},
		)

		t.Run("shows the cells in columns", func(t *testing.T) {
			m := New().SetSize(80, 3).SetRows(cellRows(
				[]string{"info", "started"},
				[]string{"error", "failed to connect"},
			))

			require.Equal(
				t,
				stamp+"info  started\n"+stamp+"error failed to connect\n",
				m.View(),
			)
		})

		t.Run(
			"shows the raw line when every cell is empty",
			func(t *testing.T) {
				m := New().SetSize(80, 2).SetRows(cellRows(
					[]string{"info", "started"},
					[]string{"", ""},
				))

				require.Equal(t, stamp+"info started\n"+stamp+"raw", m.View())
			},
		)

		t.Run("shows a cell next to an empty one", func(t *testing.T) {
			m := New().SetSize(80, 2).SetRows(cellRows(
				[]string{"", "no level"},
				[]string{"warn", ""},
			))

			require.Equal(t, stamp+"     no level\n"+stamp+"warn", m.View())
		})

		t.Run(
			"a column keeps the widest width it has shown",
			func(t *testing.T) {
				m := New().SetSize(80, 1)

				m = m.SetRows(cellRows([]string{"error", "a"}))
				m = m.SetRows(cellRows([]string{"info", "b"}))

				require.Equal(t, stamp+"info  b", m.View())
			},
		)

		t.Run("ResetWidths forgets widths", func(t *testing.T) {
			m := New().SetSize(80, 1)

			m = m.SetRows(cellRows([]string{"error", "a"}))
			m = m.SetRows(cellRows([]string{"info", "b"}))
			m = m.ResetWidths()

			require.Equal(t, stamp+"info b", m.View())
		})

		t.Run("raw rows don't widen columns", func(t *testing.T) {
			rows := cellRows([]string{"info", "a"}, []string{"", ""})
			rows[1].Raw = "a very long raw line"

			m := New().SetSize(80, 2).SetRows(rows)

			require.Equal(
				t,
				stamp+"info a\n"+stamp+"a very long raw line",
				m.View(),
			)
		})

		t.Run("caps a column at a quarter of the width", func(t *testing.T) {
			m := New().SetSize(40, 1).SetRows(cellRows(
				[]string{"0123456789abcdef", "end"},
			))

			// The quarter of 40 is 10 cells.
			require.Equal(t, stamp+"012345678… end", m.View())
		})

		t.Run("the message column's cap has a floor", func(t *testing.T) {
			long := strings.Repeat("m", 60)
			rows := cellRows([]string{long, "end"})

			// A quarter of 100 is 25 cells, below the floor of 40.
			plain := New().SetSize(100, 1).SetRows(rows)
			message := New().SetSize(100, 1).SetMessageColumn(0).SetRows(rows)

			require.Equal(
				t,
				stamp+strings.Repeat("m", 24)+"… end",
				plain.View(),
			)
			require.Equal(
				t,
				stamp+strings.Repeat("m", 39)+"… end",
				message.View(),
			)
		})

		t.Run(
			"the message column keeps a quarter when that's wider",
			func(t *testing.T) {
				long := strings.Repeat("m", 100)
				m := New().SetSize(200, 1).SetMessageColumn(0).
					SetRows(cellRows([]string{long, "end"}))

				// A quarter of 200 is 50 cells, above the floor.
				require.Equal(
					t,
					stamp+strings.Repeat("m", 49)+"… end",
					m.View(),
				)
			},
		)

		t.Run("the last column takes the rest and is cut", func(t *testing.T) {
			m := New().SetSize(len(stamp)+10, 1).SetRows(cellRows(
				[]string{"x", "0123456789abc"},
			))

			view := m.View()

			require.Equal(t, stamp+"x 0123456…", view)
			require.Equal(t, len(stamp)+10, ansi.StringWidth(view))
		})

		t.Run(
			"has no ellipsis when the cut falls in a gap",
			func(t *testing.T) {
				rows := cellRows([]string{"ab", "cd", "ef"})

				// The first column ends at len(stamp)+2, then comes its separator.
				for _, extra := range []int{2, 3} {
					m := New().SetSize(len(stamp)+extra, 1).SetRows(rows)

					require.Equal(
						t,
						stamp+"ab",
						strings.TrimRight(m.View(), " "),
					)
					require.NotContains(t, m.View(), ellipsis)
				}
			},
		)

		t.Run(
			"has no ellipsis when the last visible value fits",
			func(t *testing.T) {
				m := New().SetSize(len(stamp)+5, 1).SetRows(cellRows(
					[]string{"ab", "cd", "ef"},
				))

				require.Equal(t, stamp+"ab cd", m.View())
			},
		)

		t.Run(
			"has an ellipsis when a middle column is cut",
			func(t *testing.T) {
				m := New().SetSize(len(stamp)+4, 1).SetRows(cellRows(
					[]string{"ab", "cdef", "gh"},
				))

				require.Equal(t, stamp+"ab …", m.View())
			},
		)

		t.Run("cuts lines to the width", func(t *testing.T) {
			m := New().SetSize(len(stamp)+8, 1).SetRows(rawRows("0123456789"))

			require.Equal(t, stamp+"0123456…", m.View())
		})

		t.Run("cuts a narrow pane inside the timestamp", func(t *testing.T) {
			m := New().SetSize(10, 1).SetRows(rawRows("x"))

			require.Equal(t, "1970-01-0…", m.View())
		})

		t.Run("cuts wide characters by cells", func(t *testing.T) {
			m := New().SetSize(len(stamp)+5, 1).SetRows(rawRows("日本語日本語"))

			require.Equal(t, stamp+"日本…", m.View())
		})

		t.Run("ignores rows past the height", func(t *testing.T) {
			m := New().SetSize(60, 2).SetRows(rawRows("a", "b", "c"))

			require.Equal(t, stamp+"a\n"+stamp+"b", m.View())
		})

		t.Run("is empty without a size", func(t *testing.T) {
			m := New().SetRows(rawRows("a"))

			require.Empty(t, m.View())
		})

		t.Run("replaces escape sequences", func(t *testing.T) {
			m := New().SetSize(80, 2).SetRows([]logstore.Row{
				{Raw: "\x1b[31mred\x1b[0m\x07", Values: []string{""}},
				{Raw: "x", Values: []string{"\x1b[31mred\x07", "\x1b]0;t\x07"}},
			})
			view := m.View()

			require.NotContains(t, view, "\x1b")
			require.NotContains(t, view, "\x07")
			require.Equal(
				t,
				stamp+"�[31mred�[0m�\n"+stamp+"�[31mred� �]0;t�",
				view,
			)
		})

		t.Run("replaces C1 controls and invalid UTF-8", func(t *testing.T) {
			m := New().SetSize(80, 1).
				SetRows(cellRows([]string{"a\u009bb\xffc"}))

			require.Equal(t, stamp+"a�b�c", m.View())
		})

		t.Run("replaces tabs with spaces", func(t *testing.T) {
			m := New().SetSize(80, 1).SetRows(cellRows([]string{"a\tb"}))

			require.Equal(t, stamp+"a"+strings.Repeat(" ", 4)+"b", m.View())
		})
	})

	t.Run("SetRows", func(t *testing.T) {
		t.Run("returns a new model and keeps the original", func(t *testing.T) {
			m := New().SetSize(10, 1)
			next := m.SetRows(rawRows("a"))

			require.Empty(t, m.Rows())
			require.Len(t, next.Rows(), 1)
		})

		t.Run("keeps the original's widths", func(t *testing.T) {
			m := New().SetSize(80, 1).SetRows(cellRows([]string{"a", "b"}))
			_ = m.SetRows(cellRows([]string{"wide value", "b"}))

			require.Equal(t, stamp+"a b", m.View())
		})
	})
}
