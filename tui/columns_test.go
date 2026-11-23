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
		{
			"no level or message",
			[]string{"a", "b", "_raw"},
			[]string{"a", "b", "_raw"},
		},
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
		{"only a level", []string{"a", "lvl"}, []string{"lvl", "a"}},
		{
			"only a message",
			[]string{"a", "message"},
			[]string{"message", "a"},
		},
		{
			"other keys follow in order of appearance",
			[]string{"a", "msg", "b", "level", "c"},
			[]string{"level", "msg", "a", "b", "c"},
		},
		{
			"matches without regard to case, and keeps the key's case",
			[]string{"Level", "MSG"},
			[]string{"Level", "MSG"},
		},
		{
			"a higher priority candidate wins over an earlier key",
			[]string{"lvl", "severity", "level", "text", "msg"},
			[]string{"level", "msg", "lvl", "severity", "text"},
		},
		{
			"the first of keys that differ by case leads",
			[]string{"LEVEL", "level"},
			[]string{"LEVEL", "level"},
		},
		{
			"every candidate is recognized",
			[]string{"levelname", "event"},
			[]string{"levelname", "event"},
		},
		{
			"a dotted key doesn't match",
			[]string{"log.level", "http.msg"},
			[]string{"log.level", "http.msg"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, defaultKeys(tt.known))
		})
	}
}

func TestMessageIndex(t *testing.T) {
	require.Equal(t, 1, messageIndex([]string{"level", "msg", "a"}))
	require.Equal(t, 0, messageIndex([]string{"Message"}))
	require.Equal(t, -1, messageIndex([]string{"_raw"}))
	require.Equal(t, -1, messageIndex(nil))
}
