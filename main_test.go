package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want config
		ok   bool
	}{
		{
			"a file is read once",
			[]string{"app.log"},
			config{path: "app.log"},
			true,
		},
		{
			"-f follows the file",
			[]string{"-f", "app.log"},
			config{path: "app.log", follow: true},
			true,
		},
		{
			"-f after the file is not a flag",
			[]string{"app.log", "-f"},
			config{},
			false,
		},
		{"-f needs a file", []string{"-f"}, config{}, false},
		{"unknown flag", []string{"-x", "app.log"}, config{}, false},
		{"two files", []string{"a.log", "b.log"}, config{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseArgs(tt.args)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}
