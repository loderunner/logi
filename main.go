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
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/loderunner/logi/config"
	"github.com/loderunner/logi/ingest"
	"github.com/loderunner/logi/logstore"
	"github.com/loderunner/logi/tui"
)

// lineBuffer is the capacity of the channel from the reader to ingest.
const lineBuffer = 4096

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load(os.Args[1:])
	if errors.Is(err, config.ErrHelp) {
		fmt.Fprint(os.Stderr, config.Usage())
		return 0
	}

	var usageErr *config.UsageError
	if errors.As(err, &usageErr) {
		fmt.Fprintf(os.Stderr, "logi: %v\n\n%s", err, config.Usage())
		return 2
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "logi:", err)
		return 1
	}

	// Load can't check this, because it is about the process's stdin, not
	// the configuration.
	if cfg.LogFile == "" && term.IsTerminal(os.Stdin.Fd()) {
		fmt.Fprintf(
			os.Stderr,
			"logi: no file, and stdin is a terminal\n\n%s",
			config.Usage(),
		)

		return 2
	}

	err = start(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logi:", err)
		return 1
	}

	return 0
}

// start runs logi on the file at cfg.LogFile, or on stdin if it is empty.
func start(cfg config.Config) (err error) {
	path := cfg.LogFile

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := logstore.Open(
		ctx,
		logstore.Options{MemoryLimit: cfg.MemoryLimit},
	)
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
		if cfg.Follow {
			fail(ingest.ReadFollow(ctx, input, lines))
		} else {
			fail(ingest.ReadAll(ctx, input, lines))
		}
	}()

	ingestDone := make(chan error, 1)

	go func() {
		runErr := ingest.Run(
			ctx,
			lines,
			writer,
			ingest.Options{TimeField: cfg.TimeField},
			func(i ingest.Ingested) { p.Send(i) },
		)
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
