// Command genlogs writes synthetic JSONL application logs to stdout.
//
// By default it streams a few lines per second until interrupted, like
// `docker logs -f` or `tail -f`. Run it with `>> app.log` to append to a file
// that logi follows, or pipe it into logi. With -size-mb it instead writes that
// much output as fast as it can and exits, to make a file to load.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "genlogs:", err)
		os.Exit(1)
	}
}

func run() error {
	sizeMB := flag.Int(
		"size-mb",
		0,
		"write this many MiB as fast as possible, then exit; 0 streams until interrupted",
	)
	rate := flag.Float64(
		"rate",
		5,
		"average lines per second when streaming",
	)
	seed := flag.Uint64("seed", 0, "seed for the generator")
	start := flag.String(
		"start-time",
		"",
		"RFC-3339 timestamp for the first line (default now); "+
			"when streaming, the clock then runs in real time from there",
	)
	flag.Parse()

	if *sizeMB < 0 {
		return fmt.Errorf("-size-mb must not be negative, got %d", *sizeMB)
	}

	var startTime time.Time
	if *start != "" {
		var err error

		startTime, err = time.Parse(time.RFC3339Nano, *start)
		if err != nil {
			return fmt.Errorf("invalid start time %q: %w", *start, err)
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

func runStream(rate float64, seed uint64, start time.Time) error {
	if rate <= 0 {
		return fmt.Errorf("-rate must be positive, got %g", rate)
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	return stream(ctx, os.Stdout, options{Seed: seed, Rate: rate, Start: start})
}
