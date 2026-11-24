// Command genlogs writes synthetic JSONL application logs to stdout, for tests
// and for trying logi on realistic input.
//
// By default it streams a few lines per second until interrupted, like
// `docker logs -f` or `tail -f`. Run it with `>> app.log` to append to a file
// that logi follows, or pipe it into logi. With -size-mb it instead writes that
// much output as fast as it can and exits, to make a file to load. With
// -start-time, the first line gets that time, and a stream then runs its clock
// in real time from there.
//
// The output has nested objects, an array field, keys that appear on only some
// lines, about 2% out-of-order timestamps and about 0.1% lines that aren't
// JSON.
//
// If the reader closes the pipe, genlogs exits with status 0, as it does on
// SIGINT.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// By default, the runtime kills a process whose write to stdout hits a
	// closed pipe, with a "broken pipe" signal that shells and task runners
	// report as a failure. Ignoring SIGPIPE makes the write return EPIPE
	// instead, which is how a reader such as logi says it has quit.
	signal.Ignore(syscall.SIGPIPE)

	err := run()
	if errors.Is(err, syscall.EPIPE) {
		return
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "genlogs:", err)
		os.Exit(1)
	}
}

func run() error {
	sizeMB := flag.Int(
		"size-mb",
		0,
		"write this many MiB, then exit; 0 streams until interrupted",
	)
	rate := flag.Float64(
		"rate",
		5,
		"lines per second when streaming",
	)
	seed := flag.Uint64("seed", 0, "random seed")
	start := flag.String(
		"start-time",
		"",
		"time of the first line, in RFC 3339 (default now)",
	)
	flag.Parse()

	if *sizeMB < 0 {
		return fmt.Errorf("-size-mb must be 0 or more, got %d", *sizeMB)
	}

	var startTime time.Time
	if *start != "" {
		var err error

		startTime, err = time.Parse(time.RFC3339Nano, *start)
		if err != nil {
			return fmt.Errorf(
				"-start-time %q isn't an RFC 3339 time, such as 2026-01-02T15:04:05Z",
				*start,
			)
		}
	}

	if *sizeMB == 0 {
		return runStream(*rate, *seed, startTime)
	}

	return write(os.Stdout, options{
		Size:  int64(*sizeMB) << 20,
		Seed:  *seed,
		Start: startTime,
	})
}

// runStream streams until the process gets SIGINT or SIGTERM, which is a normal
// end.
func runStream(rate float64, seed uint64, start time.Time) error {
	if rate <= 0 {
		return fmt.Errorf("-rate must be greater than 0, got %g", rate)
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	return stream(ctx, os.Stdout, options{Seed: seed, Rate: rate, Start: start})
}
