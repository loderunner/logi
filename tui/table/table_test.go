package table

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/loderunner/logi/logstore"
)

// stamp is the timestamp column of a row with the zero TS.
const stamp = "0001-01-01 00:00:00.000 "

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

// shown returns the view without styling, and without the padding that fills
// each line. Tests that check styling use View instead.
func shown(m Model) string {
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}

	return strings.Join(lines, "\n")
}

// belowTop returns the view without its top line, which is the selected row,
// and so without the reverse video that would hide the other styling.
func belowTop(m Model) string {
	_, rest, _ := strings.Cut(m.View(), "\n")

	return rest
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
					shown(m),
				)
			},
		)

		t.Run(
			"formats the timestamp in UTC with milliseconds",
			func(t *testing.T) {
				rows := rawRows("x")
				rows[0].TS = time.Date(
					2026,
					3,
					4,
					5,
					6,
					7,
					89_000_000,
					time.UTC,
				)

				m := New().SetSize(60, 1).SetRows(rows)

				require.Equal(t, "2026-03-04 05:06:07.089 x", shown(m))
			},
		)

		t.Run("converts a time in another zone to UTC", func(t *testing.T) {
			rows := rawRows("x")
			rows[0].TS = time.Date(
				2026, 3, 4, 7, 6, 7, 0, time.FixedZone("UTC+2", 2*60*60),
			)

			m := New().SetSize(60, 1).SetRows(rows)

			require.Equal(t, "2026-03-04 05:06:07.000 x", shown(m))
		})

		t.Run("shows the cells in columns", func(t *testing.T) {
			m := New().SetSize(80, 3).SetRows(cellRows(
				[]string{"info", "started"},
				[]string{"error", "failed to connect"},
			))

			require.Equal(
				t,
				stamp+"info  started\n"+stamp+"error failed to connect\n",
				shown(m),
			)
		})

		t.Run(
			"shows the raw line when every cell is empty",
			func(t *testing.T) {
				m := New().SetSize(80, 2).SetRows(cellRows(
					[]string{"info", "started"},
					[]string{"", ""},
				))

				require.Equal(t, stamp+"info started\n"+stamp+"raw", shown(m))
			},
		)

		t.Run("shows a cell next to an empty one", func(t *testing.T) {
			m := New().SetSize(80, 2).SetRows(cellRows(
				[]string{"", "no level"},
				[]string{"warn", ""},
			))

			require.Equal(t, stamp+"     no level\n"+stamp+"warn", shown(m))
		})

		t.Run("colors the known levels", func(t *testing.T) {
			m := New().SetSize(80, 6).SetLevelColumn(0).SetRows(cellRows(
				[]string{"top", "-"},
				[]string{"error", "a"},
				[]string{"WARNING", "b"},
				[]string{"Info", "c"},
				[]string{"debug", "d"},
				[]string{"notice", "e"},
			))

			// Padding stays outside the color, and the widest level is
			// "WARNING".
			want := stamp + "\x1b[38;5;1merror\x1b[m   a\n" +
				stamp + "\x1b[38;5;3mWARNING\x1b[m b\n" +
				stamp + "\x1b[38;5;4mInfo\x1b[m    c\n" +
				stamp + "\x1b[38;5;8mdebug\x1b[m   d\n" +
				stamp + "notice  e"

			require.Equal(t, want, belowTop(m))
		})

		t.Run("colors a level that is the last column", func(t *testing.T) {
			m := New().SetSize(80, 2).SetLevelColumn(1).SetRows(cellRows(
				[]string{"top", "-"},
				[]string{"a", "error"},
			))

			require.Equal(t, stamp+"a   \x1b[38;5;1merror\x1b[m", belowTop(m))
		})

		t.Run("colors a level that is cut short", func(t *testing.T) {
			// A quarter of 27 cells caps the level at 6, and the pane has 3
			// cells left after the timestamp, so "warning" shows as "wa…".
			m := New().SetSize(27, 2).SetLevelColumn(0).SetRows(cellRows(
				[]string{"top", "-"},
				[]string{"warning", "a"},
			))

			require.Equal(t, stamp+"wa…", ansi.Strip(belowTop(m)))
			require.Contains(t, belowTop(m), "\x1b[38;5;3m")
		})

		t.Run("doesn't color other columns", func(t *testing.T) {
			m := New().SetSize(80, 2).SetLevelColumn(1).SetRows(cellRows(
				[]string{"top", "-"},
				[]string{"error", "info"},
			))

			require.Equal(
				t,
				stamp+"error \x1b[38;5;4minfo\x1b[m",
				belowTop(m),
			)
		})

		t.Run("doesn't color without a level column", func(t *testing.T) {
			m := New().SetSize(80, 1).SetRows(cellRows(
				[]string{"error", "a"},
			))

			require.Equal(t, stamp+"error a", shown(m))
		})

		t.Run(
			"a column keeps the widest width it has shown",
			func(t *testing.T) {
				m := New().SetSize(80, 1)

				m = m.SetRows(cellRows([]string{"error", "a"}))
				m = m.SetRows(cellRows([]string{"info", "b"}))

				require.Equal(t, stamp+"info  b", shown(m))
			},
		)

		t.Run("ResetWidths forgets widths", func(t *testing.T) {
			m := New().SetSize(80, 1)

			m = m.SetRows(cellRows([]string{"error", "a"}))
			m = m.SetRows(cellRows([]string{"info", "b"}))
			m = m.ResetWidths()

			require.Equal(t, stamp+"info b", shown(m))
		})

		t.Run("raw rows don't widen columns", func(t *testing.T) {
			rows := cellRows([]string{"info", "a"}, []string{"", ""})
			rows[1].Raw = "a very long raw line"

			m := New().SetSize(80, 2).SetRows(rows)

			require.Equal(
				t,
				stamp+"info a\n"+stamp+"a very long raw line",
				shown(m),
			)
		})

		t.Run("caps a column at a quarter of the width", func(t *testing.T) {
			m := New().SetSize(40, 1).SetRows(cellRows(
				[]string{"0123456789abcdef", "end"},
			))

			// The quarter of 40 is 10 cells.
			require.Equal(t, stamp+"012345678… end", shown(m))
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
				shown(plain),
			)
			require.Equal(
				t,
				stamp+strings.Repeat("m", 39)+"… end",
				shown(message),
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
					shown(m),
				)
			},
		)

		t.Run("the last column takes the rest and is cut", func(t *testing.T) {
			m := New().SetSize(len(stamp)+10, 1).SetRows(cellRows(
				[]string{"x", "0123456789abc"},
			))

			view := shown(m)

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
						strings.TrimRight(shown(m), " "),
					)
					require.NotContains(t, shown(m), ellipsis)
				}
			},
		)

		t.Run(
			"has no ellipsis when the last visible value fits",
			func(t *testing.T) {
				m := New().SetSize(len(stamp)+5, 1).SetRows(cellRows(
					[]string{"ab", "cd", "ef"},
				))

				require.Equal(t, stamp+"ab cd", shown(m))
			},
		)

		t.Run(
			"has an ellipsis when a middle column is cut",
			func(t *testing.T) {
				m := New().SetSize(len(stamp)+4, 1).SetRows(cellRows(
					[]string{"ab", "cdef", "gh"},
				))

				require.Equal(t, stamp+"ab …", shown(m))
			},
		)

		t.Run("cuts lines to the width", func(t *testing.T) {
			m := New().SetSize(len(stamp)+8, 1).SetRows(rawRows("0123456789"))

			require.Equal(t, stamp+"0123456…", shown(m))
		})

		t.Run("cuts a narrow pane inside the timestamp", func(t *testing.T) {
			m := New().SetSize(10, 1).SetRows(rawRows("x"))

			require.Equal(t, "0001-01-0…", shown(m))
		})

		t.Run("cuts wide characters by cells", func(t *testing.T) {
			m := New().SetSize(len(stamp)+5, 1).SetRows(rawRows("日本語日本語"))

			require.Equal(t, stamp+"日本…", shown(m))
		})

		t.Run("ignores rows past the height", func(t *testing.T) {
			m := New().SetSize(60, 2).SetRows(rawRows("a", "b", "c"))

			require.Equal(t, stamp+"a\n"+stamp+"b", shown(m))
		})

		t.Run("is empty without a size", func(t *testing.T) {
			m := New().SetRows(rawRows("a"))

			require.Empty(t, shown(m))
		})

		t.Run("replaces escape sequences", func(t *testing.T) {
			m := New().SetSize(80, 2).SetRows([]logstore.Row{
				{Raw: "\x1b[31mred\x1b[0m\x07", Values: []string{""}},
				{
					Raw:    "x",
					Values: []string{"\x1b[31mred\x07", "\x1b]0;t\x07"},
				},
			})
			view := shown(m)

			require.NotContains(t, view, "\x1b")
			require.NotContains(t, view, "\x07")
			require.Equal(
				t,
				stamp+"�[31mred�[0m�\n"+stamp+"�[31mred� �]0;t�",
				view,
			)
		})
	})

	t.Run("Selected", func(t *testing.T) {
		t.Run("is the top row", func(t *testing.T) {
			m := New().SetSize(60, 2).SetRows(rawRows("newest", "older"))

			row, ok := m.Selected()

			require.True(t, ok)
			require.Equal(t, "newest", row.Raw)
		})

		t.Run("is none when the table is empty", func(t *testing.T) {
			m := New().SetSize(60, 2)

			_, ok := m.Selected()

			require.False(t, ok)
		})
	})

	t.Run("selected row", func(t *testing.T) {
		t.Run("is in reverse video, padded to the width", func(t *testing.T) {
			m := New().SetSize(40, 2).SetRows(rawRows("newest", "older"))

			top, below, _ := strings.Cut(m.View(), "\n")

			require.Equal(
				t,
				"\x1b[7m"+stamp+"newest"+strings.Repeat(" ", 40-len(stamp)-6)+
					"\x1b[m",
				top,
			)
			require.Equal(t, stamp+"older", below)
		})

		t.Run("is padded to the width when cut", func(t *testing.T) {
			m := New().SetSize(len(stamp)+4, 1).SetRows(rawRows("0123456789"))

			require.Equal(t, "\x1b[7m"+stamp+"012…\x1b[m", m.View())
		})

		t.Run("reverses the whole level cell in its color", func(t *testing.T) {
			m := New().SetSize(40, 2).SetLevelColumn(0).SetRows(cellRows(
				[]string{"info", "a"},
				[]string{"error", "b"},
			))

			top, below, _ := strings.Cut(m.View(), "\n")

			require.Equal(
				t,
				"\x1b[7m"+stamp+"\x1b[m"+
					"\x1b[7;38;5;4minfo \x1b[m"+
					"\x1b[7m a"+strings.Repeat(" ", 40-len(stamp)-7)+"\x1b[m",
				top,
			)
			require.Equal(t, stamp+"\x1b[38;5;1merror\x1b[m b", below)
		})

		t.Run(
			"reverses a level that is the last column to the edge",
			func(t *testing.T) {
				m := New().SetSize(40, 1).SetLevelColumn(1).SetRows(cellRows(
					[]string{"a", "warn"},
				))

				require.Equal(
					t,
					"\x1b[7m"+stamp+"a \x1b[m"+
						"\x1b[7;38;5;3mwarn"+strings.Repeat(" ", 40-len(stamp)-6)+
						"\x1b[m",
					m.View(),
				)
			},
		)

		t.Run("keeps an unknown level in plain reverse", func(t *testing.T) {
			m := New().SetSize(40, 1).SetLevelColumn(0).SetRows(cellRows(
				[]string{"notice", "a"},
			))

			require.Equal(
				t,
				"\x1b[7m"+stamp+"notice a"+strings.Repeat(" ", 40-len(stamp)-8)+
					"\x1b[m",
				m.View(),
			)
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

			require.Equal(t, stamp+"a b", shown(m))
		})
	})
}
