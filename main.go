// Command logi is a terminal UI for JSONL logs. It reads a file, or stdin
// when there is no argument. With -f or --follow it keeps reading as the file
// grows.
//
//	logi app.log
//	logi -f app.log
//	kubectl logs -f <pod> | logi
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/pflag"

	"github.com/loderunner/logi/ingest"
	"github.com/loderunner/logi/logstore"
	"github.com/loderunner/logi/tui"
)

const (
	// memoryLimit is DuckDB's memory limit.
	memoryLimit = "1GB"
	// lineBuffer is the capacity of the channel from the reader to ingest.
	lineBuffer = 4096
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := parseArgs(os.Args[1:])
	if errors.Is(err, pflag.ErrHelp) {
		fmt.Fprint(os.Stderr, usage())
		return 0
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "logi: %v\n\n%s", err, usage())
		return 2
	}

	err = start(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logi:", err)
		return 1
	}

	return 0
}

// config is what the command line asks for.
type config struct {
	// path is the file to read, or "" for stdin.
	path string
	// follow keeps reading a file as it grows.
	follow bool
}

// newFlags returns the flag set that writes into cfg. It is the one place that
// declares flags, so the usage can't drift from what parseArgs accepts.
//
// Flags stop at the first argument that isn't a flag, so "logi app.log -f" is
// an error instead of a surprise. pflag's own error printing is off: run
// decides what to print and where.
func newFlags(cfg *config) *pflag.FlagSet {
	flags := pflag.NewFlagSet("logi", pflag.ContinueOnError)
	flags.SetInterspersed(false)
	flags.SetOutput(io.Discard)
	flags.BoolVarP(
		&cfg.follow, "follow", "f", false, "keep reading as the file grows",
	)

	return flags
}

// usage describes the command line, with the flags listed by pflag.
func usage() string {
	return "usage: logi [flags] [file]\n" +
		"With no file, logi reads standard input.\n\n" +
		newFlags(&config{}).FlagUsages()
}

// parseArgs reads the command line. It returns pflag.ErrHelp when help is
// requested, and another error when the arguments are wrong, including -f
// without a file: stdin is always read to its end.
func parseArgs(args []string) (config, error) {
	var cfg config

	flags := newFlags(&cfg)

	err := flags.Parse(args)
	if err != nil {
		return config{}, err
	}

	switch flags.NArg() {
	case 0:
		if cfg.follow {
			return config{}, errors.New("--follow needs a file")
		}

		if term.IsTerminal(os.Stdin.Fd()) {
			return config{}, errors.New("no file, and stdin is a terminal")
		}

		return cfg, nil
	case 1:
		cfg.path = flags.Arg(0)
		return cfg, nil
	default:
		return config{}, fmt.Errorf(
			"expected at most one file, got %d", flags.NArg(),
		)
	}
}

// start runs logi on the file at cfg.path, or on stdin if the path is empty.
func start(cfg config) (err error) {
	path := cfg.path

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := logstore.Open(ctx, logstore.Options{MemoryLimit: memoryLimit})
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, store.Close())
	}()

	opts := tui.Options{Stdin: path == ""}
	input := os.Stdin

	if path != "" {
		input, err = os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = input.Close() }()

		info, statErr := input.Stat()
		if statErr != nil {
			return statErr
		}

		if info.IsDir() {
			return fmt.Errorf("%s is a directory", path)
		}

		opts.FileSize = info.Size()
	}

	writer, err := store.NewWriter(ctx)
	if err != nil {
		return err
	}

	p := tea.NewProgram(tui.New(store, opts))
	lines := make(chan ingest.Line, lineBuffer)

	// A failure in a goroutine ends the program, and Run's caller reports it.
	fail := func(err error) {
		if err != nil {
			p.Send(tui.ErrMsg{Err: err})
		}
	}

	go func() {
		if cfg.follow {
			fail(ingest.ReadFollow(ctx, input, lines))
		} else {
			fail(ingest.ReadAll(ctx, input, lines))
		}
	}()

	ingestDone := make(chan error, 1)

	go func() {
		runErr := ingest.Run(ctx, lines, writer, func(i ingest.Ingested) {
			p.Send(i)
		})
		closeErr := writer.Close()

		err := errors.Join(runErr, closeErr)
		fail(err)

		ingestDone <- err
	}()

	final, runErr := p.Run()

	// Stop the goroutines and wait for ingest, which closes the Writer.
	// Don't wait for ReadAll: a blocked read on stdin can't be interrupted,
	// and the process is about to exit.
	cancel()

	ingestErr := <-ingestDone

	if runErr != nil {
		return runErr
	}

	model, ok := final.(tui.Model)
	if ok && model.Err() != nil {
		return model.Err()
	}

	return ingestErr
}
