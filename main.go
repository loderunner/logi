// Command logi is a terminal UI for JSONL logs. It reads a file, or stdin
// when there is no argument. With -f it keeps reading as the file grows.
//
//	logi app.log
//	logi -f app.log
//	kubectl logs -f <pod> | logi
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/loderunner/logi/ingest"
	"github.com/loderunner/logi/logstore"
	"github.com/loderunner/logi/tui"
)

const (
	usage = "usage: logi [-f] [file]\n" +
		"With no file, logi reads standard input.\n" +
		"  -f  keep reading as the file grows\n"

	// memoryLimit is DuckDB's memory limit.
	memoryLimit = "1GB"
	// lineBuffer is the capacity of the channel from the reader to ingest.
	lineBuffer = 4096
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, ok := parseArgs(os.Args[1:])
	if !ok {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	err := start(cfg)
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

// parseArgs reads the command line. It reports false when the arguments are
// wrong, including -f without a file: stdin is always read to its end.
func parseArgs(args []string) (config, bool) {
	flags := flag.NewFlagSet("logi", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	var cfg config

	flags.BoolVar(&cfg.follow, "f", false, "")

	err := flags.Parse(args)
	if err != nil {
		return config{}, false
	}

	switch flags.NArg() {
	case 0:
		if cfg.follow || term.IsTerminal(os.Stdin.Fd()) {
			return config{}, false
		}

		return cfg, true
	case 1:
		cfg.path = flags.Arg(0)
		return cfg, true
	default:
		return config{}, false
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
