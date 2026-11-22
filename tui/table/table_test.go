package table

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/loderunner/logi/logstore"
)

func rowsOf(raws ...string) []logstore.Row {
	rows := make([]logstore.Row, len(raws))
	for i, raw := range raws {
		rows[i] = logstore.Row{ID: int64(i), Raw: raw}
	}

	return rows
}

func TestModel(t *testing.T) {
	t.Run("View", func(t *testing.T) {
		t.Run(
			"shows one line per row, padded to the height",
			func(t *testing.T) {
				m := New().SetSize(20, 4).SetRows(rowsOf("newest", "older"))

				require.Equal(t, "newest\nolder\n\n", m.View())
			},
		)

		t.Run("cuts lines to the width", func(t *testing.T) {
			m := New().SetSize(8, 1).SetRows(rowsOf("0123456789"))

			require.Equal(t, "0123456…", m.View())
		})

		t.Run("cuts wide characters by cells", func(t *testing.T) {
			m := New().SetSize(5, 1).SetRows(rowsOf("日本語日本語"))

			require.Equal(t, "日本…", m.View())
		})

		t.Run("ignores rows past the height", func(t *testing.T) {
			m := New().SetSize(10, 2).SetRows(rowsOf("a", "b", "c"))

			require.Equal(t, "a\nb", m.View())
		})

		t.Run("is empty without a size", func(t *testing.T) {
			m := New().SetRows(rowsOf("a"))

			require.Empty(t, m.View())
		})

		t.Run("replaces escape sequences", func(t *testing.T) {
			m := New().SetSize(40, 1).SetRows(rowsOf("\x1b[31mred\x1b[0m\x07"))
			view := m.View()

			require.NotContains(t, view, "\x1b")
			require.NotContains(t, view, "\x07")
			require.Equal(t, "�[31mred�[0m�", view)
		})

		t.Run("replaces C1 controls and invalid UTF-8", func(t *testing.T) {
			m := New().SetSize(40, 1).SetRows(rowsOf("a\u009bb\xffc"))

			require.Equal(t, "a�b�c", m.View())
		})

		t.Run("replaces tabs with spaces", func(t *testing.T) {
			m := New().SetSize(40, 1).SetRows(rowsOf("a\tb"))

			require.Equal(t, "a"+strings.Repeat(" ", 4)+"b", m.View())
		})
	})

	t.Run("SetRows", func(t *testing.T) {
		t.Run("returns a new model and keeps the original", func(t *testing.T) {
			m := New().SetSize(10, 1)
			next := m.SetRows(rowsOf("a"))

			require.Empty(t, m.Rows())
			require.Len(t, next.Rows(), 1)
		})
	})
}
