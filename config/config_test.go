package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/loderunner/logi/config"
)

// isolate points the environment at an empty home, so that the tests don't
// read the developer's config file or LOGI_ variables. It returns the
// directory that XDG_CONFIG_HOME names.
func isolate(t *testing.T) string {
	t.Helper()

	xdg := t.TempDir()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("LOGI_CONFIG", "")

	return xdg
}

// writeFile writes content to dir/name, creating directories, and returns the
// path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)

	err := os.MkdirAll(filepath.Dir(path), 0o755)
	require.NoError(t, err)

	err = os.WriteFile(path, []byte(content), 0o600)
	require.NoError(t, err)

	return path
}

func TestLoadArgs(t *testing.T) {
	// errUsage stands for any *UsageError.
	errUsage := &config.UsageError{}

	tests := []struct {
		name string
		args []string
		want config.Config
		// err is nil when the arguments are right, config.ErrHelp when help
		// is requested, and errUsage for any other error.
		err error
	}{
		{
			"a file is read once",
			[]string{"app.log"},
			config.Config{LogFile: "app.log"},
			nil,
		},
		{"no file is stdin", nil, config.Config{}, nil},
		{
			"-f follows the file",
			[]string{"-f", "app.log"},
			config.Config{LogFile: "app.log", Follow: true},
			nil,
		},
		{
			"--follow follows the file",
			[]string{"--follow", "app.log"},
			config.Config{LogFile: "app.log", Follow: true},
			nil,
		},
		{
			"-f after the file is not a flag",
			[]string{"app.log", "-f"},
			config.Config{},
			errUsage,
		},
		{"-f needs a file", []string{"-f"}, config.Config{}, errUsage},
		{
			"--follow needs a file",
			[]string{"--follow"},
			config.Config{},
			errUsage,
		},
		{
			"unknown flag",
			[]string{"-x", "app.log"},
			config.Config{},
			errUsage,
		},
		{
			"two files",
			[]string{"a.log", "b.log"},
			config.Config{},
			errUsage,
		},
		{"-h asks for help", []string{"-h"}, config.Config{}, config.ErrHelp},
		{
			"--help asks for help",
			[]string{"--help"},
			config.Config{},
			config.ErrHelp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolate(t)

			got, err := config.Load(tt.args)

			var usage *config.UsageError

			switch {
			case tt.err == nil:
				require.NoError(t, err)
			case errors.Is(tt.err, config.ErrHelp):
				require.ErrorIs(t, err, config.ErrHelp)
				require.NotErrorAs(t, err, &usage)
			default:
				require.ErrorAs(t, err, &usage)
				require.NotErrorIs(t, err, config.ErrHelp)
			}

			require.Equal(t, tt.want, got)
		})
	}
}

func TestLoadConfigFile(t *testing.T) {
	t.Run("--config names the file", func(t *testing.T) {
		isolate(t)

		path := writeFile(t, t.TempDir(), "any.name", `{}`)

		got, err := config.Load([]string{"--config", path, "app.log"})

		require.NoError(t, err)
		require.Equal(t, path, got.Config)
		require.Equal(t, "app.log", got.LogFile)
	})

	t.Run("LOGI_CONFIG names the file", func(t *testing.T) {
		isolate(t)

		path := writeFile(t, t.TempDir(), "c.json", `{}`)
		t.Setenv("LOGI_CONFIG", path)

		got, err := config.Load([]string{"app.log"})

		require.NoError(t, err)
		require.Equal(t, path, got.Config)
	})

	t.Run("--config beats LOGI_CONFIG", func(t *testing.T) {
		isolate(t)

		dir := t.TempDir()
		flagPath := writeFile(t, dir, "flag.json", `{}`)
		envPath := writeFile(t, dir, "env.json", `{}`)
		t.Setenv("LOGI_CONFIG", envPath)

		got, err := config.Load([]string{"--config", flagPath, "app.log"})

		require.NoError(t, err)
		require.Equal(t, flagPath, got.Config)
	})

	t.Run("a missing --config file is an error", func(t *testing.T) {
		isolate(t)

		missing := filepath.Join(t.TempDir(), "nope.json")

		_, err := config.Load([]string{"--config", missing, "app.log"})

		require.ErrorIs(t, err, os.ErrNotExist)
		require.ErrorContains(t, err, missing)
		require.NotErrorAs(t, err, new(*config.UsageError))
	})

	t.Run("a missing LOGI_CONFIG file is an error", func(t *testing.T) {
		isolate(t)

		missing := filepath.Join(t.TempDir(), "nope.json")
		t.Setenv("LOGI_CONFIG", missing)

		_, err := config.Load([]string{"app.log"})

		require.ErrorIs(t, err, os.ErrNotExist)
		require.ErrorContains(t, err, missing)
	})

	t.Run("a missing implicit file is fine", func(t *testing.T) {
		isolate(t)

		got, err := config.Load([]string{"app.log"})

		require.NoError(t, err)
		require.Empty(t, got.Config)
	})

	t.Run("the implicit file is in XDG_CONFIG_HOME", func(t *testing.T) {
		xdg := isolate(t)

		path := writeFile(t, xdg, "logi/config.json", `{}`)

		got, err := config.Load([]string{"app.log"})

		require.NoError(t, err)
		require.Equal(t, path, got.Config)
	})

	t.Run("the implicit file is in ~/.config without XDG", func(t *testing.T) {
		isolate(t)

		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")

		path := writeFile(t, home, ".config/logi/config.json", `{}`)

		got, err := config.Load([]string{"app.log"})

		require.NoError(t, err)
		require.Equal(t, path, got.Config)
	})

	t.Run("XDG_CONFIG_HOME beats HOME", func(t *testing.T) {
		xdg := isolate(t)

		home := t.TempDir()
		t.Setenv("HOME", home)

		writeFile(t, home, ".config/logi/config.json", `{}`)

		want := writeFile(t, xdg, "logi/config.json", `{}`)

		got, err := config.Load([]string{"app.log"})

		require.NoError(t, err)
		require.Equal(t, want, got.Config)
	})

	t.Run("an explicit file hides the implicit one", func(t *testing.T) {
		xdg := isolate(t)

		// The implicit file is invalid, so reading it would fail.
		writeFile(t, xdg, "logi/config.json", `not json`)

		path := writeFile(t, t.TempDir(), "c.json", `{}`)

		got, err := config.Load([]string{"--config", path, "app.log"})

		require.NoError(t, err)
		require.Equal(t, path, got.Config)
	})

	t.Run("invalid JSON in --config is an error", func(t *testing.T) {
		isolate(t)

		path := writeFile(t, t.TempDir(), "bad.json", `{"a": `)

		_, err := config.Load([]string{"--config", path, "app.log"})

		require.ErrorContains(t, err, path)
		require.NotErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("invalid JSON in the implicit file is an error", func(t *testing.T) {
		xdg := isolate(t)

		path := writeFile(t, xdg, "logi/config.json", `not json`)

		_, err := config.Load([]string{"app.log"})

		require.ErrorContains(t, err, path)
	})

	t.Run(
		"without a home directory there is no implicit file",
		func(t *testing.T) {
			isolate(t)
			t.Setenv("HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")

			got, err := config.Load([]string{"app.log"})

			require.NoError(t, err)
			require.Empty(t, got.Config)
		},
	)
}

func TestLoadTimeField(t *testing.T) {
	// setup makes every level name a different value, and returns the
	// --config path. The implicit file is in XDG_CONFIG_HOME.
	setup := func(t *testing.T) (explicit string) {
		t.Helper()

		xdg := isolate(t)

		writeFile(t, xdg, "logi/config.json", `{"time-field": "implicit"}`)
		t.Setenv("LOGI_TIME_FIELD", "env")

		return writeFile(t, t.TempDir(), "c.json", `{"time-field": "explicit"}`)
	}

	t.Run("the default is empty", func(t *testing.T) {
		isolate(t)

		got, err := config.Load([]string{"app.log"})

		require.NoError(t, err)
		require.Empty(t, got.TimeField)
	})

	t.Run("the flag beats everything", func(t *testing.T) {
		explicit := setup(t)

		got, err := config.Load(
			[]string{"--config", explicit, "--time-field", "flag", "app.log"},
		)

		require.NoError(t, err)
		require.Equal(t, "flag", got.TimeField)
	})

	t.Run("the environment beats the files", func(t *testing.T) {
		explicit := setup(t)

		got, err := config.Load([]string{"--config", explicit, "app.log"})

		require.NoError(t, err)
		require.Equal(t, "env", got.TimeField)
	})

	t.Run("an explicit file beats the implicit one", func(t *testing.T) {
		explicit := setup(t)
		t.Setenv("LOGI_TIME_FIELD", "")

		got, err := config.Load([]string{"--config", explicit, "app.log"})

		require.NoError(t, err)
		require.Equal(t, "explicit", got.TimeField)
	})

	t.Run("an explicit file hides the implicit one", func(t *testing.T) {
		xdg := isolate(t)

		writeFile(t, xdg, "logi/config.json", `{"time-field": "implicit"}`)

		explicit := writeFile(t, t.TempDir(), "c.json", `{}`)

		got, err := config.Load([]string{"--config", explicit, "app.log"})

		require.NoError(t, err)
		require.Empty(t, got.TimeField)
	})

	t.Run("the implicit file beats the default", func(t *testing.T) {
		setup(t)
		t.Setenv("LOGI_TIME_FIELD", "")

		got, err := config.Load([]string{"app.log"})

		require.NoError(t, err)
		require.Equal(t, "implicit", got.TimeField)
	})

	t.Run("a dotted key is kept as it is", func(t *testing.T) {
		xdg := isolate(t)

		writeFile(t, xdg, "logi/config.json", `{"time-field": "log.time"}`)

		got, err := config.Load([]string{"app.log"})

		require.NoError(t, err)
		require.Equal(t, "log.time", got.TimeField)
	})
}

func TestUsage(t *testing.T) {
	got := config.Usage()

	require.Contains(t, got, "usage: logi")
	require.Contains(t, got, "-f, --follow")
	require.Contains(t, got, "--config")
	require.Contains(t, got, "--time-field")
}
