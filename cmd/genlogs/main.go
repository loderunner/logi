// Command genlogs writes synthetic JSONL application logs to stdout, then
// exits. Run it with `>> app.log` to append to a file that logi follows.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	sizeMB := flag.Int("size-mb", 1, "approximate size of the output, in MiB")
	seed := flag.Uint64("seed", 0, "seed for the generator")
	start := flag.String(
		"start-time",
		"",
		"RFC-3339 timestamp for the first log line",
	)
	flag.Parse()

	var startTime time.Time
	if *start != "" {
		var err error

		startTime, err = time.Parse(time.RFC3339Nano, *start)
		if err != nil {
			fmt.Fprintf(
				os.Stderr,
				"genlogs: invalid start time %q: %v\n",
				*start,
				err,
			)
			os.Exit(1)
		}
	}

	err := write(os.Stdout, options{
		Size:  int64(*sizeMB) << 20,
		Seed:  *seed,
		Start: startTime,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "genlogs:", err)
		os.Exit(1)
	}
}
