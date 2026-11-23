package ingest

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFlatten(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantVals   string
		wantFields map[string]string
	}{
		{
			name:     "flat strings",
			line:     `{"level":"info","msg":"hello world"}`,
			wantVals: "info hello world",
			wantFields: map[string]string{
				"level": "info",
				"msg":   "hello world",
			},
		},
		{
			name:     "nested objects become dotted keys",
			line:     `{"http":{"status":200,"req":{"path":"/a"}},"ok":true}`,
			wantVals: "200 /a true",
			wantFields: map[string]string{
				"http.status":   "200",
				"http.req.path": "/a",
				"ok":            "true",
			},
		},
		{
			name:       "keys after a nested object keep the outer prefix",
			line:       `{"a":{"b":"1"},"c":"2"}`,
			wantVals:   "1 2",
			wantFields: map[string]string{"a.b": "1", "c": "2"},
		},
		{
			name:     "arrays are compact JSON text, and set doesn't look inside",
			line:     `{"tags": [ "a", {"b": 1},  null ] , "z":"1"}`,
			wantVals: `["a",{"b":1},null] 1`,
			wantFields: map[string]string{
				"tags": `["a",{"b":1},null]`,
				"z":    "1",
			},
		},
		{
			name:     "strings are words in vals and decoded text in fields",
			line:     `{"m":"  Hello,  world! (a-b)\t\u00a0can't\n\u0000x ","n":"!!!","o":"y"}`,
			wantVals: "Hello, world! (a-b) can't x !!! y",
			wantFields: map[string]string{
				"m": "  Hello,  world! (a-b)\t\u00a0can't\n\u0000x ",
				"n": "!!!",
				"o": "y",
			},
		},
		{
			name:     "numbers keep their literal text",
			line:     `{"n":1.50,"big":12345678901234567890,"exp":1e3,"neg":-0}`,
			wantVals: "1.50 12345678901234567890 1e3 -0",
			wantFields: map[string]string{
				"n":   "1.50",
				"big": "12345678901234567890",
				"exp": "1e3",
				"neg": "-0",
			},
		},
		{
			name:       "booleans",
			line:       `{"a":true,"b":false}`,
			wantVals:   "true false",
			wantFields: map[string]string{"a": "true", "b": "false"},
		},
		{
			name:       "null has no field",
			line:       `{"a":null,"b":"x"}`,
			wantVals:   "x",
			wantFields: map[string]string{"b": "x"},
		},
		{
			name:       "empty object has no field",
			line:       `{"a":{},"b":{"c":{}}}`,
			wantVals:   "",
			wantFields: map[string]string{},
		},
		{
			name:       "the empty line object has no fields",
			line:       `{}`,
			wantVals:   "",
			wantFields: map[string]string{},
		},
		{
			name:       "escapes are decoded",
			line:       `{"m":"a\"b\\c\ndé😀"}`,
			wantVals:   "a\"b\\c dé😀",
			wantFields: map[string]string{"m": "a\"b\\c\ndé😀"},
		},
		{
			name:       "escaped keys are decoded",
			line:       `{"a\tb":"x"}`,
			wantVals:   "x",
			wantFields: map[string]string{"a\tb": "x"},
		},
		{
			name:       "invalid UTF-8 is accepted",
			line:       "{\"m\":\"a\xffb\"}",
			wantVals:   "a�b",
			wantFields: map[string]string{"m": "a�b"},
		},
		{
			name:       "the last of duplicate keys wins",
			line:       `{"a":"1","b":"x","a":"2"}`,
			wantVals:   "1 x 2",
			wantFields: map[string]string{"a": "2", "b": "x"},
		},
		{
			name:       "an empty string is a field with no vals text",
			line:       `{"a":"","b":"x"}`,
			wantVals:   "x",
			wantFields: map[string]string{"a": "", "b": "x"},
		},
		{
			name:       "surrounding whitespace is fine",
			line:       "  {\"a\":\"1\"} \t",
			wantVals:   "1",
			wantFields: map[string]string{"a": "1"},
		},
		{
			name:       "a top-level array is raw",
			line:       `["a"]`,
			wantVals:   `["a"]`,
			wantFields: map[string]string{"_raw": `["a"]`},
		},
		{
			name:       "a top-level string is raw",
			line:       `"a"`,
			wantVals:   `"a"`,
			wantFields: map[string]string{"_raw": `"a"`},
		},
		{
			name:       "a top-level number is raw",
			line:       `42`,
			wantVals:   `42`,
			wantFields: map[string]string{"_raw": `42`},
		},
		{
			name:       "plain text is raw",
			line:       `starting server on :8080`,
			wantVals:   `starting server on :8080`,
			wantFields: map[string]string{"_raw": `starting server on :8080`},
		},
		{
			name:       "a truncated object is raw, with no partial fields",
			line:       `{"a":"1","b":`,
			wantVals:   `{"a":"1","b":`,
			wantFields: map[string]string{"_raw": `{"a":"1","b":`},
		},
		{
			name:       "garbage after the object is raw",
			line:       `{"a":"1"} trailing`,
			wantVals:   `{"a":"1"} trailing`,
			wantFields: map[string]string{"_raw": `{"a":"1"} trailing`},
		},
		{
			name:       "a second object is raw",
			line:       `{"a":"1"}{"b":"2"}`,
			wantVals:   `{"a":"1"}{"b":"2"}`,
			wantFields: map[string]string{"_raw": `{"a":"1"}{"b":"2"}`},
		},
		{
			name:       "an empty line is raw",
			line:       ``,
			wantVals:   ``,
			wantFields: map[string]string{"_raw": ``},
		},
	}

	// One flattener serves every case, as in Run, so a line can't leak state
	// into the next one.
	f := newFlattener()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals, fields := f.flatten([]byte(tt.line))

			require.Equal(t, tt.wantVals, vals)
			require.Equal(t, tt.wantFields, fields)
		})
	}

	t.Run("vals follow document order", func(t *testing.T) {
		vals, _ := f.flatten([]byte(`{"z":"1","a":{"y":"2","b":"3"},"m":"4"}`))

		require.Equal(t, "1 2 3 4", vals)
	})
}
