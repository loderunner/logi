package logstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/duckdb/duckdb-go/v2"
)

// fixedColumns is the number of columns before the key columns: id, ts, raw
// and vals.
const fixedColumns = 4

// Writer appends rows to the logs table with a DuckDB Appender on a dedicated
// connection. It is not safe for concurrent use. Rows are visible to queries
// after Flush.
//
// The Appender's columns are fixed when it opens, so when a row has a key the
// table hasn't seen, the Writer flushes, closes the Appender, adds the
// columns and opens a new Appender.
type Writer struct {
	store *Store
	conn  *sql.Conn
	app   *duckdb.Appender

	// keys lists the keys with a column, in column order after the fixed
	// columns.
	keys []string
	// pos maps a key to its index in keys.
	pos map[string]int
	// added lists the keys added since the last Flush.
	added []string
	// row is the buffer for the values of one row.
	row []driver.Value
}

// NewWriter opens a Writer. Close it before closing the Store.
func (s *Store) NewWriter(ctx context.Context) (*Writer, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("get connection: %w", err)
	}

	w := &Writer{store: s, conn: conn, pos: make(map[string]int)}

	err = w.openAppender()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	return w, nil
}

func (w *Writer) openAppender() error {
	err := w.conn.Raw(func(driverConn any) error {
		var appErr error

		w.app, appErr = duckdb.NewAppenderFromConn(
			driverConn.(driver.Conn),
			"",
			"logs",
		)

		return appErr
	})
	if err != nil {
		return fmt.Errorf("open appender: %w", err)
	}

	return nil
}

// Append adds a row with the value of each key in fields. A key that no row
// has had before gets a new column, and earlier rows have no value for it.
// The row isn't visible to queries until Flush.
func (w *Writer) Append(
	id, ts int64,
	raw, vals string,
	fields map[string]string,
) error {
	err := w.addColumns(fields)
	if err != nil {
		return err
	}

	w.row = w.row[:0]
	w.row = append(w.row, id, ts, validUTF8(raw), validUTF8(vals))

	for range w.keys {
		w.row = append(w.row, nil)
	}

	for key, value := range fields {
		w.row[fixedColumns+w.pos[key]] = validUTF8(value)
	}

	err = w.app.AppendRow(w.row...)
	if err != nil {
		return fmt.Errorf("append row: %w", err)
	}

	return nil
}

// validUTF8 replaces each invalid byte of s with U+FFFD. The Appender turns
// a VARCHAR that isn't valid UTF-8 into NULL without an error, and logs
// can contain any bytes.
func validUTF8(s string) string {
	return strings.ToValidUTF8(s, "\uFFFD")
}

// addColumns adds a column for each key in fields that has none, in sorted
// order so the result doesn't depend on map iteration.
func (w *Writer) addColumns(fields map[string]string) error {
	var missing []string

	for key := range fields {
		_, ok := w.pos[key]
		if !ok {
			missing = append(missing, key)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	slices.Sort(missing)

	// Closing the Appender flushes it, so the rows it holds are stored
	// before the table changes.
	err := w.app.Close()
	if err != nil {
		return fmt.Errorf("close appender: %w", err)
	}

	for _, key := range missing {
		name := "k" + strconv.Itoa(len(w.keys)+1)

		_, err = w.conn.ExecContext(
			context.Background(),
			fmt.Sprintf("ALTER TABLE logs ADD COLUMN %s VARCHAR", name),
		)
		if err != nil {
			return fmt.Errorf("add column for key %q: %w", key, err)
		}

		w.pos[key] = len(w.keys)
		w.keys = append(w.keys, key)
		w.added = append(w.added, key)

		w.store.mu.Lock()
		w.store.columns[key] = name
		w.store.mu.Unlock()
	}

	return w.openAppender()
}

// Flush makes the appended rows visible to queries. It returns the keys that
// got a column since the last call, in the order they were added, and
// forgets them.
func (w *Writer) Flush() ([]string, error) {
	err := w.app.Flush()
	if err != nil {
		return nil, fmt.Errorf("flush appender: %w", err)
	}

	added := w.added
	w.added = nil

	return added, nil
}

// Close flushes the appender and releases the connection.
func (w *Writer) Close() error {
	appErr := w.app.Close()
	connErr := w.conn.Close()

	return errors.Join(appErr, connErr)
}
