package main

import (
	"errors"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

// errUsage stands for any error that is not pflag.ErrHelp.
var errUsage = errors.New("usage error")

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want config
		// err is nil when the arguments are right, pflag.ErrHelp when help is
		// requested, and errUsage for any other error.
		err error
	}{
		{
			"a file is read once",
			[]string{"app.log"},
			config{path: "app.log"},
			nil,
		},
		{
			"-f follows the file",
			[]string{"-f", "app.log"},
			config{path: "app.log", follow: true},
			nil,
		},
		{
			"--follow follows the file",
			[]string{"--follow", "app.log"},
			config{path: "app.log", follow: true},
			nil,
		},
		{
			"-f after the file is not a flag",
			[]string{"app.log", "-f"},
			config{},
			errUsage,
		},
		{"-f needs a file", []string{"-f"}, config{}, errUsage},
		{"--follow needs a file", []string{"--follow"}, config{}, errUsage},
		{"unknown flag", []string{"-x", "app.log"}, config{}, errUsage},
		{"two files", []string{"a.log", "b.log"}, config{}, errUsage},
		{"-h asks for help", []string{"-h"}, config{}, pflag.ErrHelp},
		{"--help asks for help", []string{"--help"}, config{}, pflag.ErrHelp},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args)

			switch tt.err {
			case nil:
				require.NoError(t, err)
			case errUsage:
				require.Error(t, err)
				require.NotErrorIs(t, err, pflag.ErrHelp)
			default:
				require.ErrorIs(t, err, tt.err)
			}

			require.Equal(t, tt.want, got)
		})
	}
}

func TestUsage(t *testing.T) {
	got := usage()

	require.Contains(t, got, "usage: logi")
	require.Contains(t, got, "-f, --follow")
}
