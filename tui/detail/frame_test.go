package detail

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestFrame(t *testing.T) {
	t.Run("draws a rounded border with the title", func(t *testing.T) {
		m := New().SetSize(16, 5).Open("Title", `{"a":1}`)

		want := "╭─ Title ──────╮\n" +
			"│ {            │\n" +
			"│   \"a\": 1     │\n" +
			"│ }            │\n" +
			"╰──────────────╯"
		require.Equal(t, want, ansi.Strip(m.View()))
	})

	t.Run("wraps the text inside the frame", func(t *testing.T) {
		m := New().SetSize(12, 6).Open("", `{"a":"0123456"}`)

		want := "╭──────────╮\n" +
			"│ {        │\n" +
			"│   \"a\": \" │\n" +
			"│ 0123456\" │\n" +
			"│ }        │\n" +
			"╰──────────╯"
		require.Equal(t, want, ansi.Strip(m.View()))
	})

	t.Run("cuts a title too long for the top edge", func(t *testing.T) {
		m := New().SetSize(12, 4).Open("Detail: 2026-11-25", "x")

		want := "╭─ Detai… ─╮\n" +
			"│ x        │\n" +
			"│          │\n" +
			"╰──────────╯"
		require.Equal(t, want, ansi.Strip(m.View()))
	})

	t.Run("page keys scroll by the height inside", func(t *testing.T) {
		// Nine lines, three of them inside a pane of five.
		m := New().SetSize(20, 5).Open("", `{"a":[1,2,3,4,5]}`)

		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		require.Equal(t, "    2,\n    3,\n    4,", ansi.Strip(m.body()))
	})

	t.Run("is dropped when the pane is too small", func(t *testing.T) {
		for _, size := range [][2]int{{9, 10}, {40, 3}} {
			m := New().SetSize(size[0], size[1]).Open("Title", "x")

			require.NotContains(t, m.View(), "╭", size)
			require.Equal(t, m.body(), m.View(), size)
		}
	})
}
