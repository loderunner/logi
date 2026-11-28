package filterbar

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// typed returns m after the keys that type text.
func typed(t *testing.T, m Model, text string) Model {
	t.Helper()

	for _, r := range text {
		var cmd tea.Cmd

		m, cmd = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		_ = cmd
	}

	return m
}

// focused returns an empty bar that has focus, 20 columns wide.
func focused(t *testing.T) Model {
	t.Helper()

	m, _ := New().SetWidth(20).Focus()

	return m
}

func TestModel(t *testing.T) {
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	esc := tea.KeyPressMsg{Code: tea.KeyEscape}

	t.Run("View", func(t *testing.T) {
		t.Run(
			"shows the prompt and a placeholder while empty",
			func(t *testing.T) {
				m := New().SetWidth(20)

				require.Equal(
					t,
					"/ field:value       ",
					ansi.Strip(m.View()),
				)
			},
		)

		t.Run("shows the prompt and the text", func(t *testing.T) {
			m := typed(t, focused(t), "level:error")

			require.Equal(
				t,
				"/ level:error       ",
				ansi.Strip(m.View()),
			)
		})

		t.Run(
			"makes the prompt bold with focus and faint without",
			func(t *testing.T) {
				m := focused(t)
				require.Contains(t, m.View(), "\x1b[1m/ ")
				require.NotContains(t, m.View(), "\x1b[2m/ ")

				m = m.Blur()
				require.Contains(t, m.View(), "\x1b[2m/ ")
				require.NotContains(t, m.View(), "\x1b[1m/ ")
			},
		)

		t.Run("makes the placeholder faint", func(t *testing.T) {
			for _, m := range []Model{New().SetWidth(20), focused(t)} {
				require.Contains(t, m.View(), "\x1b[2mield:value")
			}
		})

		t.Run("cuts the text to the width", func(t *testing.T) {
			m := typed(t, focused(t), strings.Repeat("a", 30))

			for _, line := range strings.Split(m.View(), "\n") {
				require.LessOrEqual(t, ansi.StringWidth(line), 20)
			}
		})

		t.Run("cuts a bar narrower than its prompt", func(t *testing.T) {
			m := typed(t, focused(t), "abc").SetWidth(1)

			require.LessOrEqual(t, ansi.StringWidth(m.View()), 1)
		})

		t.Run("shows the error in red under the bar", func(t *testing.T) {
			m := focused(t).SetError("unexpected }")

			lines := strings.Split(m.View(), "\n")
			require.Len(t, lines, 2)
			require.Equal(t, "unexpected }", ansi.Strip(lines[1]))
			require.Contains(t, lines[1], "\x1b[38;5;1m")
		})

		t.Run("makes the error safe to print and cuts it", func(t *testing.T) {
			m := New().SetWidth(10).SetError("bad \x1b[31m token, really")

			lines := strings.Split(m.View(), "\n")
			require.Equal(t, "bad �[31m…", ansi.Strip(lines[1]))
			require.LessOrEqual(t, ansi.StringWidth(lines[1]), 10)
		})
	})

	t.Run("Focus and Blur", func(t *testing.T) {
		m := New()
		require.False(t, m.input.Focused())

		m, _ = m.Focus()
		require.True(t, m.input.Focused())
		require.False(t, m.Blur().input.Focused())
	})

	t.Run("Cursor", func(t *testing.T) {
		t.Run(
			"is the terminal's, after the text, while focused",
			func(t *testing.T) {
				m := typed(t, focused(t), "abc")

				c := m.Cursor()
				require.NotNil(t, c)
				require.Equal(t, 2+3, c.X)
				require.Equal(t, 0, c.Y)
			},
		)

		t.Run("is hidden without focus", func(t *testing.T) {
			require.Nil(t, typed(t, focused(t), "abc").Blur().Cursor())
			require.Nil(t, New().Cursor())
		})

		t.Run("starts no blink timer", func(t *testing.T) {
			_, cmd := New().Focus()
			require.Nil(t, cmd)
		})
	})

	t.Run("Update", func(t *testing.T) {
		t.Run("ignores keys without focus", func(t *testing.T) {
			m := typed(t, New().SetWidth(20), "abc")

			require.Empty(t, m.Value())
		})

		t.Run("types q, slash and spaces", func(t *testing.T) {
			m := typed(t, focused(t), "q / x")

			require.Equal(t, "q / x", m.Value())
		})

		t.Run("edits the text", func(t *testing.T) {
			m := typed(t, focused(t), "level:erro")
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})

			require.Equal(t, "level:er", m.Value())
		})

		t.Run("makes a pasted newline and tab a space", func(t *testing.T) {
			m, _ := focused(t).Update(tea.PasteMsg{Content: "a\nb\tc\x1b[31m"})

			require.Equal(t, "a b c[31m", m.Value())
		})

		t.Run("ignores a Tab key", func(t *testing.T) {
			m := typed(t, focused(t), "a")
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})

			require.Equal(t, "a", m.Value())
		})
	})

	t.Run("Enter", func(t *testing.T) {
		t.Run("asks to apply the text", func(t *testing.T) {
			m := typed(t, focused(t), "level:error")

			next, cmd := m.Update(enter)
			require.NotNil(t, cmd)
			require.Equal(t, SubmitMsg{Text: "level:error"}, cmd())

			// The bar keeps its text and focus until the parent decides.
			require.Equal(t, "level:error", next.Value())
			require.NotNil(t, next.Cursor())
		})

		t.Run("does nothing without focus", func(t *testing.T) {
			_, cmd := New().Update(enter)

			require.Nil(t, cmd)
		})
	})

	t.Run("Commit and Esc", func(t *testing.T) {
		t.Run("Esc restores the committed text", func(t *testing.T) {
			m := typed(t, focused(t), "level:error").Commit()
			m = typed(t, m, " x")
			require.Equal(t, "level:error x", m.Value())

			m, cmd := m.Update(esc)
			require.Equal(t, "level:error", m.Value())
			require.NotNil(t, cmd)
			require.Equal(t, CancelMsg{}, cmd())

			// The cursor is at the end, so typing goes on from there.
			m = typed(t, m, "!")
			require.Equal(t, "level:error!", m.Value())
		})

		t.Run("Esc before a Commit empties the bar", func(t *testing.T) {
			m, _ := typed(t, focused(t), "abc").Update(esc)

			require.Empty(t, m.Value())
		})

		t.Run("Commit keeps the text", func(t *testing.T) {
			m := typed(t, focused(t), "abc").Commit()

			require.Equal(t, "abc", m.Value())
		})

		t.Run("Esc clears the error", func(t *testing.T) {
			m, _ := typed(t, focused(t), "abc").SetError("bad").Update(esc)

			require.Equal(t, 1, m.Height())
		})

		t.Run("Esc does nothing without focus", func(t *testing.T) {
			_, cmd := New().Update(esc)

			require.Nil(t, cmd)
		})
	})

	t.Run("errors", func(t *testing.T) {
		t.Run("Height is 1, or 2 with an error", func(t *testing.T) {
			m := New()
			require.Equal(t, 1, m.Height())

			m = m.SetError("bad")
			require.Equal(t, 2, m.Height())

			m = m.SetError("")
			require.Equal(t, 1, m.Height())
		})

		t.Run("an edit clears the error", func(t *testing.T) {
			for name, msg := range map[string]tea.Msg{
				"a character": tea.KeyPressMsg{Code: 'x', Text: "x"},
				"a delete":    tea.KeyPressMsg{Code: tea.KeyBackspace},
				"a paste":     tea.PasteMsg{Content: "x"},
			} {
				m := typed(t, focused(t), "ab").SetError("bad")

				m, _ = m.Update(msg)
				require.Equal(t, 1, m.Height(), name)
			}
		})

		t.Run("a cursor move keeps the error", func(t *testing.T) {
			for name, msg := range map[string]tea.Msg{
				"left":  tea.KeyPressMsg{Code: tea.KeyLeft},
				"right": tea.KeyPressMsg{Code: tea.KeyRight},
				"home":  tea.KeyPressMsg{Code: tea.KeyHome},
				"end":   tea.KeyPressMsg{Code: tea.KeyEnd},
			} {
				m := typed(t, focused(t), "ab").SetError("bad")

				m, _ = m.Update(msg)
				require.Equal(t, 2, m.Height(), name)
			}
		})

		t.Run("a key that changes nothing keeps the error", func(t *testing.T) {
			// Backspace at the start of the text deletes nothing.
			m := typed(t, focused(t), "ab").SetError("bad")
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})

			require.Equal(t, "ab", m.Value())
			require.Equal(t, 2, m.Height())
		})
	})
}
