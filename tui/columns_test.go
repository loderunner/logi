package tui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultKeys(t *testing.T) {
	tests := []struct {
		name  string
		known []string
		want  []string
	}{
		{"nothing known", nil, []string{"_raw"}},
		{"no level or message", []string{"a", "b", "_raw"}, []string{"_raw"}},
		{
			"level and message",
			[]string{"level", "msg"},
			[]string{"level", "msg"},
		},
		{
			"level comes first whatever the order of appearance",
			[]string{"msg", "level"},
			[]string{"level", "msg"},
		},
		{"only a level", []string{"a", "lvl"}, []string{"lvl"}},
		{"only a message", []string{"a", "message"}, []string{"message"}},
		{
			"matches without regard to case, and keeps the key's case",
			[]string{"Level", "MSG"},
			[]string{"Level", "MSG"},
		},
		{
			"a higher priority candidate wins over an earlier key",
			[]string{"lvl", "severity", "level", "text", "msg"},
			[]string{"level", "msg"},
		},
		{
			"the first of keys that differ by case wins",
			[]string{"LEVEL", "level"},
			[]string{"LEVEL"},
		},
		{
			"every candidate is recognized",
			[]string{"levelname", "event"},
			[]string{"levelname", "event"},
		},
		{
			"a dotted key doesn't match",
			[]string{"log.level", "http.msg"},
			[]string{"_raw"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, defaultKeys(tt.known))
		})
	}
}
