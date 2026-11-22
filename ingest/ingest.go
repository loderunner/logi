package ingest

import (
	"context"
	"fmt"
	"time"
)

const (
	// flushRows is how many appended rows trigger a flush.
	flushRows = 10_000
	// flushInterval is the longest a row waits before a flush.
	flushInterval = 100 * time.Millisecond
)

// Appender stores rows. *logstore.Writer satisfies it. Rows must be visible
// to queries after Flush.
//
// The Appender owns the schema: it adds a column the first time it sees a key
// in fields. Flush returns the keys it has added since the last Flush, in the
// order it added them.
type Appender interface {
	Append(id, ts int64, raw, vals string, fields map[string]string) error
	Flush() ([]string, error)
}

// Ingested reports progress after each flush.
type Ingested struct {
	// Rows is the number of rows stored so far.
	Rows int64
	// Bytes is the input offset just after the last stored row.
	Bytes int64
	// NewKeys lists the keys stored for the first time since the last report.
	NewKeys []string
	// EOF is true on the last report, when the input has ended.
	EOF bool
}

// Run stores the lines it receives. It parses each line as a JSON object and
// flattens it into fields (see flattener.flatten); a line that isn't JSON has
// the single field "_raw". It gives each row the next id, starting at 1, and
// the time it arrived as its timestamp in Unix nanoseconds, so rows order by
// arrival.
//
// Run flushes after 10,000 unflushed rows or every 100ms, whichever comes
// first, then calls notify with the keys the flush added. It skips a flush
// when there are no new rows. When lines closes, it flushes and calls notify
// with EOF set. When ctx ends, it flushes and returns nil.
func Run(
	ctx context.Context,
	lines <-chan Line,
	w Appender,
	notify func(Ingested),
) error {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	parser := newFlattener()

	var (
		total   int64
		lastEnd int64
		pending int
	)

	flush := func() error {
		if pending == 0 {
			return nil
		}

		newKeys, err := w.Flush()
		if err != nil {
			return fmt.Errorf("flush: %w", err)
		}

		pending = 0

		notify(Ingested{Rows: total, Bytes: lastEnd, NewKeys: newKeys})

		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return flush()
		case <-ticker.C:
			err := flush()
			if err != nil {
				return err
			}
		case line, ok := <-lines:
			if !ok {
				err := flush()
				if err != nil {
					return err
				}

				notify(Ingested{Rows: total, Bytes: lastEnd, EOF: true})

				return nil
			}

			total++
			lastEnd = line.End

			vals, fields := parser.flatten(line.Text)

			err := w.Append(
				total,
				time.Now().UnixNano(),
				string(line.Text),
				vals,
				fields,
			)
			if err != nil {
				return fmt.Errorf("append: %w", err)
			}

			pending++
			if pending >= flushRows {
				err = flush()
				if err != nil {
					return err
				}
			}
		}
	}
}
