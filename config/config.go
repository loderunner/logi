// Package config decides what logi should do from its command line, its
// LOGI_ environment variables and a JSON config file.
//
// A setting comes from the first of these that has it: the flag, the
// environment variable, the config file, then the default. The file and -f are
// not configurable from the environment or the config file: LOGI_FOLLOW=1
// would break "kubectl logs | logi".
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const (
	// envPrefix prefixes the name of every environment variable, so that
	// the flag "time-field" reads LOGI_TIME_FIELD.
	envPrefix = "LOGI"
	// configKey is the flag, and the environment variable suffix, that name
	// the config file.
	configKey = "config"
	// timeFieldKey is the flag, the environment variable suffix and the
	// config file key that name the time field.
	timeFieldKey = "time-field"
	// memoryLimitKey is the flag, the environment variable suffix and the
	// config file key that name DuckDB's memory limit.
	memoryLimitKey = "memory-limit"
	// defaultMemoryLimit is the memory limit when none is set.
	defaultMemoryLimit = "1GB"
)

// ErrHelp is returned by Load when the command line asks for help.
var ErrHelp = pflag.ErrHelp

// UsageError reports a command line that logi can't run, so that the caller
// can print the usage next to it. Other errors, such as an unreadable config
// file, are not about the command line.
type UsageError struct {
	Err error
}

func (e *UsageError) Error() string { return e.Err.Error() }

func (e *UsageError) Unwrap() error { return e.Err }

// Config is what logi should do. Each field is named after its flag, except
// LogFile, which is the positional argument.
type Config struct {
	// LogFile is the log file to read, or "" for stdin.
	LogFile string
	// Follow keeps reading LogFile as it grows.
	Follow bool
	// Config is the config file in use, wherever it came from, or "" when
	// there is none.
	Config string
	// TimeField is the key that holds each line's time, exactly as the
	// line flattens it, such as "log.time". "" detects it.
	TimeField string
	// MemoryLimit is DuckDB's memory_limit, such as "1GB". Config doesn't
	// check it: DuckDB rejects a bad value when logstore.Open applies it.
	MemoryLimit string
}

// newFlags returns the flag set that writes into cfg. It is the one place that
// declares flags, so the usage can't drift from what Load accepts.
//
// Flags stop at the first argument that isn't a flag, so "logi app.log -f" is
// an error instead of a surprise. pflag's own error printing is off: the
// caller decides what to print and where.
func newFlags(cfg *Config) *pflag.FlagSet {
	flags := pflag.NewFlagSet("logi", pflag.ContinueOnError)
	flags.SetInterspersed(false)
	flags.SetOutput(io.Discard)
	flags.BoolVarP(
		&cfg.Follow, "follow", "f", false, "keep reading as the file grows",
	)
	flags.StringVar(
		&cfg.Config, configKey, "",
		"read this JSON config file\n"+
			"(default "+implicitConfigFile()+")",
	)
	flags.StringVar(
		&cfg.TimeField, timeFieldKey, "",
		"use this key as the time of each line, instead of\n"+
			"detecting it (case-sensitive; a nested key is dotted,\n"+
			"as in log.time)",
	)
	flags.StringVar(
		&cfg.MemoryLimit, memoryLimitKey, defaultMemoryLimit,
		"limit DuckDB's memory, such as 512MB or 2GB\n"+
			"(data over it spills to a temporary directory)",
	)

	return flags
}

// Usage describes the command line: the synopsis, the flags listed by pflag,
// then notes that apply to all of them.
func Usage() string {
	return "usage: logi [flags] [file]\n\n" +
		"flags:\n" +
		newFlags(&Config{}).FlagUsages() +
		"\n" +
		"  With no file, logi reads standard input.\n" +
		"  Each flag has an environment variable named " + envPrefix + "_*,\n" +
		"  where * is the flag's name in upper case, with hyphens as\n" +
		"  underscores.\n"
}

// Load reads the command line, the environment and the config file. It
// returns ErrHelp when help is requested, a *UsageError when the arguments are
// wrong, including -f without a file because stdin is always read to its end,
// and another error when the config file can't be read.
//
// Load doesn't check that stdin is a terminal when there is no file: that is a
// fact about the process, not about the configuration.
func Load(args []string) (Config, error) {
	var cfg Config

	flags := newFlags(&cfg)

	err := flags.Parse(args)
	if err != nil {
		if errors.Is(err, ErrHelp) {
			return Config{}, err
		}

		return Config{}, &UsageError{err}
	}

	switch flags.NArg() {
	case 0:
		if cfg.Follow {
			return Config{}, &UsageError{errors.New("--follow needs a file")}
		}
	case 1:
		cfg.LogFile = flags.Arg(0)
	default:
		return Config{}, &UsageError{fmt.Errorf(
			"expected at most one file, got %d", flags.NArg(),
		)}
	}

	// Each Load gets its own viper, so that nothing leaks between calls.
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))

	err = bind(v, flags, configKey)
	if err != nil {
		return Config{}, err
	}

	err = bind(v, flags, timeFieldKey)
	if err != nil {
		return Config{}, err
	}

	err = bind(v, flags, memoryLimitKey)
	if err != nil {
		return Config{}, err
	}

	cfg.Config, err = readConfigFile(v, v.GetString(configKey))
	if err != nil {
		return Config{}, err
	}

	cfg.TimeField = v.GetString(timeFieldKey)
	cfg.MemoryLimit = v.GetString(memoryLimitKey)

	return cfg, nil
}

// bind makes the flag with this name, and the environment variable named after
// it, a source for the viper key of the same name. Binding each key by hand,
// instead of AutomaticEnv, keeps -f out of the environment.
func bind(v *viper.Viper, flags *pflag.FlagSet, name string) error {
	err := v.BindPFlag(name, flags.Lookup(name))
	if err != nil {
		return fmt.Errorf("bind flag --%s: %w", name, err)
	}

	err = v.BindEnv(name)
	if err != nil {
		return fmt.Errorf("bind environment variable for --%s: %w", name, err)
	}

	return nil
}

// readConfigFile reads the explicit file into v, or the implicit one when
// explicit is empty. It returns the path that was read, or "" when there is
// no file. Only one file is read: an explicit file replaces the implicit one.
func readConfigFile(v *viper.Viper, explicit string) (string, error) {
	path := explicit
	if path == "" {
		path = implicitConfigFile()
	}

	if path == "" {
		return "", nil
	}

	// The type is set because the name of an explicit file can have any
	// extension.
	v.SetConfigFile(path)
	v.SetConfigType("json")

	err := v.ReadInConfig()
	// viper reports a file that SetConfigFile names but that doesn't exist as
	// the OS error, not as ConfigFileNotFoundError.
	if explicit == "" && errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("read config file %s: %w", path, err)
	}

	return path, nil
}

// implicitConfigFile returns where the config file is looked up when none is
// named. It is $XDG_CONFIG_HOME/logi/config.json, else
// ~/.config/logi/config.json, on every platform: os.UserConfigDir would pick
// ~/Library/Application Support on macOS, which is not where a terminal tool's
// users look. It returns "" when there is no home directory, which means
// that there is no implicit file.
func implicitConfigFile() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}

		dir = filepath.Join(home, ".config")
	}

	return filepath.Join(dir, "logi", "config.json")
}
