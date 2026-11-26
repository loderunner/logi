package kql_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/loderunner/logi/kql"
	"github.com/loderunner/logi/logstore"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  logstore.Filter
	}{
		{"bare", "level:error", logstore.Match{Key: "level", Value: "error"}},
		{
			"dotted key",
			"http.method:GET",
			logstore.Match{Key: "http.method", Value: "GET"},
		},
		{
			"case is kept",
			"Level:ERROR",
			logstore.Match{Key: "Level", Value: "ERROR"},
		},
		{
			"quoted value",
			`msg:"hello world"`,
			logstore.Match{Key: "msg", Value: "hello world"},
		},
		{
			"quoted field",
			`"my key":x`,
			logstore.Match{Key: "my key", Value: "x"},
		},
		{
			"quoted field and value",
			`"a b":"c d"`,
			logstore.Match{Key: "a b", Value: "c d"},
		},
		{
			"quoted field with colon",
			`"a:b":x`,
			logstore.Match{Key: "a:b", Value: "x"},
		},
		{"empty value", `level:""`, logstore.Match{Key: "level", Value: ""}},
		{
			"colon in value",
			"time:12:30",
			logstore.Match{Key: "time", Value: "12:30"},
		},
		{
			"url value",
			"url:http://x/y?a=b",
			logstore.Match{Key: "url", Value: "http://x/y?a=b"},
		},
		{
			"operators are literal",
			"q:a*(b)~<>=",
			logstore.Match{Key: "q", Value: "a*(b)~<>="},
		},
		{
			"quote inside bare value",
			`msg:a"b`,
			logstore.Match{Key: "msg", Value: `a"b`},
		},
		{
			"escaped quote",
			`msg:"say \"hi\""`,
			logstore.Match{Key: "msg", Value: `say "hi"`},
		},
		{
			"escaped backslash",
			`path:"C:\\tmp"`,
			logstore.Match{Key: "path", Value: `C:\tmp`},
		},
		{"both escapes", `m:"\\\""`, logstore.Match{Key: "m", Value: `\"`}},
		{
			"backslash in bare value",
			`path:C:\tmp`,
			logstore.Match{Key: "path", Value: `C:\tmp`},
		},
		{
			"surrounding whitespace",
			" \t level:error \n",
			logstore.Match{Key: "level", Value: "error"},
		},
		{"non-ASCII", "msg:héllo", logstore.Match{Key: "msg", Value: "héllo"}},
		{
			"non-ASCII key",
			`"clé":日本語`,
			logstore.Match{Key: "clé", Value: "日本語"},
		},
		{"empty", "", nil},
		{"whitespace only", " \t\n ", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := kql.Parse(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseError(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"level", "col 6: expected ':' after field"},
		{"level:", "col 7: expected a value"},
		{":error", "col 1: expected a field"},
		{"level : error", "col 6: expected ':' after field"},
		{`"lev`, "col 1: unterminated quote"},
		{`msg:"a\x"`, `col 7: unknown escape \x`},
		{"a:1 b:2", "col 5: expected end of filter"},
		{`level:"x"y`, "col 10: expected end of filter"},

		// Edges around the table above. The non-ASCII cases prove that
		// columns count runes.
		{"level: error", "col 7: expected a value"},
		{`msg:"a\`, "col 5: unterminated quote"},
		{`"a"b:c`, "col 4: expected ':' after field"},
		{`"a" :c`, "col 4: expected ':' after field"},
		{"héllo", "col 6: expected ':' after field"},
		{"é:", "col 3: expected a value"},
		{`é:"a\é"`, `col 5: unknown escape \é`},
		{"  :x", "col 3: expected a field"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := kql.Parse(tt.input)
			require.EqualError(t, err, tt.want)
			require.Nil(t, got)
			require.ErrorAs(t, err, new(*kql.Error))
		})
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"", " ", "level:error", "http.method:GET", `"my key":x`, `msg:"a b"`,
		`level:""`, "time:12:30", "url:http://x/y?a=b", `m:"\\\""`,
		"level", "level:", ":error", "level : error", `"lev`, `msg:"a\x"`,
		"a:1 b:2", `level:"x"y`, `msg:"a\`, "héllo:wörld",
	} {
		f.Add(s, "v")
	}

	f.Add("", `a "quoted" \ value é`)

	f.Fuzz(func(t *testing.T, input, v string) {
		_, err := kql.Parse(input)
		if err != nil {
			var kerr *kql.Error
			require.ErrorAs(t, err, &kerr)
			require.GreaterOrEqual(t, kerr.Col, 1)
			require.LessOrEqual(t, kerr.Col, utf8.RuneCountInString(input)+1)
		}

		// A quoted value round-trips through the escapes.
		if !utf8.ValidString(v) {
			return
		}

		escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v)

		got, err := kql.Parse(`f:"` + escaped + `"`)
		require.NoError(t, err)
		require.Equal(t, logstore.Match{Key: "f", Value: v}, got)
	})
}
