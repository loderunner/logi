package detail

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// Escape sequences of the palette.
const (
	reset  = "\x1b[m"
	name   = "\x1b[1;38;5;4m"
	str    = "\x1b[38;5;2m"
	number = "\x1b[38;5;3m"
	boolea = "\x1b[38;5;6m"
	null   = "\x1b[38;5;8m"
)

func TestColorize(t *testing.T) {
	colorized := func(raw string) string {
		return strings.Join(New().SetSize(80, 20).Open(raw).text, "\n")
	}

	t.Run("colors each kind of token, not the punctuation", func(t *testing.T) {
		want := "{\n" +
			"  " + name + `"s"` + reset + ": " + str + `"x"` + reset + ",\n" +
			"  " + name + `"n"` + reset + ": " + number + "1.5" + reset + ",\n" +
			"  " + name + `"t"` + reset + ": " + boolea + "true" + reset + ",\n" +
			"  " + name + `"f"` + reset + ": " + boolea + "false" + reset + ",\n" +
			"  " + name + `"z"` + reset + ": " + null + "null" + reset + "\n" +
			"}"

		require.Equal(
			t,
			want,
			colorized(`{"s":"x","n":1.5,"t":true,"f":false,"z":null}`),
		)
	})

	t.Run("colors a nested name and the items of an array", func(t *testing.T) {
		want := "{\n" +
			"  " + name + `"a"` + reset + ": {\n" +
			"    " + name + `"b"` + reset + ": [\n" +
			"      " + str + `"c"` + reset + ",\n" +
			"      " + number + "2" + reset + "\n" +
			"    ]\n" +
			"  }\n" +
			"}"

		require.Equal(t, want, colorized(`{"a":{"b":["c",2]}}`))
	})

	t.Run(
		"colors a string value that is a name's text as a string",
		func(t *testing.T) {
			want := "{\n  " + name + `"a"` + reset + ": " + str + `"a"` + reset + "\n}"

			require.Equal(t, want, colorized(`{"a":"a"}`))
		},
	)

	t.Run("colors a top-level array", func(t *testing.T) {
		want := "[\n  " + number + "1" + reset + ",\n  " +
			"{\n    " + name + `"a"` + reset + ": " + null + "null" + reset +
			"\n  }\n]"

		require.Equal(t, want, colorized(`[1,{"a":null}]`))
	})

	t.Run("colors a top-level scalar", func(t *testing.T) {
		require.Equal(t, number+"42"+reset, colorized("42"))
	})

	t.Run("replaces a raw C1 control in a string", func(t *testing.T) {
		// Some terminals read U+009B as CSI. JSON allows it in a string.
		got := colorized("{\"a\":\"\u009b\"}")

		require.Contains(t, got, "\"\uFFFD\"")
		require.NotContains(t, got, "\u009b")
	})

	t.Run("keeps an escaped control character escaped", func(t *testing.T) {
		got := colorized(`{"a":"\u009b\u001b[31m"}`)

		require.NotContains(t, got, "\u009b")
		require.NotContains(t, got, "\x1b[31m")
		require.Contains(t, got, `\u001b[31m`)
	})

	t.Run("a line that isn't JSON stays plain", func(t *testing.T) {
		got := colorized("starting up, 3 workers")

		require.Equal(t, "starting up, 3 workers", got)
	})

	t.Run("a wrapped string keeps its sequences valid", func(t *testing.T) {
		raw := `{"a":"0123456789abcdefghij","b":1}`

		colored := New().SetSize(12, 20).Open(raw)
		plain := New().SetSize(12, 20).Open(raw)
		plain.lines = wrap(
			strings.Split(ansi.Strip(strings.Join(plain.text, "\n")), "\n"),
			12,
		)

		require.Greater(t, len(colored.lines), 5)
		require.Equal(t, len(plain.lines), len(colored.lines))

		for i, line := range colored.lines {
			require.Equal(t, plain.lines[i], ansi.Strip(line))
			// Every sequence is complete: nothing but text is left after
			// stripping.
			require.NotContains(t, ansi.Strip(line), "\x1b")
		}
	})
}
