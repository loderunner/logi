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

// Appender stores rows. *logstore.Writer satisfies it.
//
// The Appender owns the schema: it adds a column the first time it sees a key
// in fields.
type Appender interface {
	// Append stores a row: its id, its time, the line's text as raw, the
	// words to search as vals and the flattened fields. A ts must be
	// representable as Unix nanoseconds, which limits it to the years 1678
	// to 2262.
	Append(
		id int64,
		ts time.Time,
		raw, vals string,
		fields map[string]string,
	) error
	// Restamp sets ts on every row with an id below beforeID, including rows
	// appended but not yet flushed.
	Restamp(beforeID int64, ts time.Time) error
	// Flush makes the appended rows visible to queries. It returns the keys
	// that Append has added as columns since the last Flush, in the order it
	// added them.
	Flush() ([]string, error)
}

// Options configures Run.
type Options struct {
	// TimeField is the key of the time field, exactly as the line flattens
	// it, such as "log.time". "" detects the field among the usual names.
	TimeField string
}

// Ingested reports progress after each flush.
type Ingested struct {
	// Rows is the number of rows stored so far.
	Rows int64
	// Bytes is the input offset just after the last stored row.
	Bytes int64
	// NewKeys lists the keys stored for the first time since the last report.
	NewKeys []string
	// TimeKey is the key of the time field, or "" until a line has fixed it.
	// Every report after that carries it.
	TimeKey string
	// EOF is true on the last report, when the input has ended.
	EOF bool
}

// Run stores the lines it receives. It parses each line as a JSON object and
// flattens it into fields (see flattener.flatten); a line that isn't JSON has
// the single field "_raw". It gives each row the next id, starting at 1, and
// a timestamp, so that rows order by (ts, id).
//
// The timestamp comes from the time field, which the first line with a
// parseable one fixes for the rest of the run (see stamper). With
// opts.TimeField, that key is the only candidate. Until the field is fixed, a
// row has its arrival time. After that, a row without a usable time takes the
// previous row's, so it sorts next to its neighbours. When the field is fixed
// on a row after the first, Run restamps the rows before it with that row's
// time, once.
//
// Run flushes after flushRows unflushed rows or every flushInterval (10,000
// rows or 100ms), whichever comes first, then calls notify with the keys the
// flush added. It skips a flush when there are no new rows. When lines
// closes, it flushes and calls notify with EOF set. When ctx ends, it flushes
// and returns without setting EOF.
func Run(
	ctx context.Context,
	lines <-chan Line,
	w Appender,
	opts Options,
	notify func(Ingested),
) error {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	parser := newFlattener()

	var (
		stamps  = stamper{field: opts.TimeField}
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

		notify(Ingested{
			Rows:    total,
			Bytes:   lastEnd,
			NewKeys: newKeys,
			TimeKey: stamps.key,
		})

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

				notify(Ingested{
					Rows:    total,
					Bytes:   lastEnd,
					TimeKey: stamps.key,
					EOF:     true,
				})

				return nil
			}

			total++
			lastEnd = line.End

			vals, fields := parser.flatten(line.Text)

			ts, detected := stamps.stamp(fields)
			if detected && total > 1 {
				err := w.Restamp(total, ts)
				if err != nil {
					return fmt.Errorf("restamp: %w", err)
				}
			}

			err := w.Append(total, ts, string(line.Text), vals, fields)
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
