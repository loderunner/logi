package detail

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModel(t *testing.T) {
	t.Run("Open", func(t *testing.T) {
		t.Run("indents with two spaces, keeping key order", func(t *testing.T) {
			m := New().SetSize(40, 8).Open(`{"b":1,"a":{"c":[true,null]}}`)

			want := "{\n" +
				"  \"b\": 1,\n" +
				"  \"a\": {\n" +
				"    \"c\": [\n" +
				"      true,\n" +
				"      null\n" +
				"    ]\n" +
				"  }\n" +
				"}"
			require.Equal(t, want, strings.Join(m.text, "\n"))
		})

		t.Run("keeps duplicate keys", func(t *testing.T) {
			m := New().SetSize(40, 4).Open(`{"a":1,"a":2}`)

			require.Equal(
				t,
				"{\n  \"a\": 1,\n  \"a\": 2\n}",
				strings.Join(m.text, "\n"),
			)
		})

		t.Run("keeps a newline in a string escaped", func(t *testing.T) {
			m := New().SetSize(40, 3).Open(`{"a":"x\ny"}`)

			require.Equal(
				t,
				"{\n  \"a\": \"x\\ny\"\n}",
				strings.Join(m.text, "\n"),
			)
		})

		t.Run("indents a top-level array", func(t *testing.T) {
			m := New().SetSize(40, 4).Open(`[1,{"a":2}]`)

			require.Equal(
				t,
				"[\n  1,\n  {\n    \"a\": 2\n  }\n]",
				strings.Join(m.text, "\n"),
			)
		})

		t.Run("shows a line that isn't JSON as is", func(t *testing.T) {
			m := New().SetSize(40, 2).Open("starting up, 3 workers")

			require.Equal(t, "starting up, 3 workers\n", m.View())
		})

		t.Run("shows a line with trailing content as is", func(t *testing.T) {
			m := New().SetSize(40, 1).Open(`{"a":1} extra`)

			require.Equal(t, `{"a":1} extra`, m.View())
		})

		t.Run("replaces control characters", func(t *testing.T) {
			m := New().SetSize(40, 1).Open("not json\t\x1b[31mred")

			require.Equal(t, "not json    �[31mred", m.View())
		})

		t.Run(
			"replaces control characters in a JSON string",
			func(t *testing.T) {
				// Indent accepts invalid UTF-8 and C1 controls in a string.
				m := New().SetSize(40, 3).Open("{\"a\":\"x\u009by\xff\"}")

				require.Equal(
					t,
					"{\n  \"a\": \"x�y�\"\n}",
					strings.Join(m.text, "\n"),
				)
			},
		)
	})

	t.Run("wraps a long line", func(t *testing.T) {
		m := New().SetSize(12, 5).Open(`{"a":"0123456789abcdef"}`)

		require.Equal(
			t,
			"{\n  \"a\": \"0123\n456789abcdef\n\"\n}",
			m.View(),
		)
	})

	t.Run("SetSize wraps the lines again", func(t *testing.T) {
		m := New().SetSize(40, 3).Open(`{"a":"0123456789"}`)
		require.Equal(t, "{\n  \"a\": \"0123456789\"\n}", m.View())

		m = m.SetSize(10, 4)
		require.Equal(t, "{\n  \"a\": \"01\n23456789\"\n}", m.View())
	})

	t.Run("a pane that has no size shows nothing", func(t *testing.T) {
		m := New().Open(`{"a":1}`)

		require.Empty(t, m.View())

		// Opening before the size is known must not lose the lines.
		m = m.SetSize(20, 3)
		require.Equal(t, "{\n  \"a\": 1\n}", m.View())
	})

	t.Run("View pads to the height", func(t *testing.T) {
		m := New().SetSize(20, 5).Open(`{"a":1}`)

		require.Equal(t, "{\n  \"a\": 1\n}\n\n", m.View())
	})

	t.Run("View cuts to the height", func(t *testing.T) {
		m := New().SetSize(20, 2).Open(`{"a":1}`)

		require.Equal(t, "{\n  \"a\": 1", m.View())
	})

	t.Run("Scroll", func(t *testing.T) {
		// Nine lines in a pane of three.
		open := func() Model {
			return New().SetSize(20, 3).Open(`{"a":[1,2,3,4,5]}`)
		}

		t.Run("moves the first line shown", func(t *testing.T) {
			m := open().Scroll(2)

			require.Equal(t, "    1,\n    2,\n    3,", m.View())

			m = m.Scroll(-1)
			require.Equal(t, "  \"a\": [\n    1,\n    2,", m.View())
		})

		t.Run("stops at the top", func(t *testing.T) {
			m := open().Scroll(-5)

			require.Equal(t, "{\n  \"a\": [\n    1,", m.View())
		})

		t.Run("stops at the last screenful", func(t *testing.T) {
			m := open().Scroll(100)

			require.Equal(t, "    5\n  ]\n}", m.View())

			// Going back up starts from the last screenful, not from 100.
			m = m.Scroll(-1)
			require.Equal(t, "    4,\n    5\n  ]", m.View())
		})

		t.Run("does nothing when the lines fit", func(t *testing.T) {
			m := New().SetSize(20, 5).Open(`{"a":1}`).Scroll(3)

			require.Equal(t, "{\n  \"a\": 1\n}\n\n", m.View())
		})
	})

	t.Run("SetSize clamps the offset", func(t *testing.T) {
		m := New().SetSize(20, 3).Open(`{"a":[1,2,3,4,5]}`).Scroll(100)

		m = m.SetSize(20, 5)
		require.Equal(t, "    3,\n    4,\n    5\n  ]\n}", m.View())

		m = m.SetSize(20, 20)
		require.Equal(
			t,
			"{\n  \"a\": [\n    1,\n    2,\n    3,\n    4,\n    5\n  ]\n}",
			strings.TrimRight(m.View(), "\n"),
		)
	})

	t.Run("Open resets the offset", func(t *testing.T) {
		m := New().SetSize(20, 3).Open(`{"a":[1,2,3,4,5]}`).Scroll(100)

		m = m.Open(`{"b":[1,2,3,4,5]}`)
		require.Equal(t, "{\n  \"b\": [\n    1,", m.View())
	})
}
