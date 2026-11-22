package logstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/duckdb/duckdb-go/v2"
)

// Writer appends rows to the logs table with a DuckDB Appender on a dedicated
// connection. It is not safe for concurrent use. Rows are visible to queries
// after Flush.
type Writer struct {
	conn *sql.Conn
	app  *duckdb.Appender
}

// NewWriter opens a Writer. Close it before closing the Store.
func (s *Store) NewWriter(ctx context.Context) (*Writer, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("get connection: %w", err)
	}

	var app *duckdb.Appender

	err = conn.Raw(func(driverConn any) error {
		var appErr error

		app, appErr = duckdb.NewAppenderFromConn(
			driverConn.(driver.Conn),
			"",
			"logs",
		)

		return appErr
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open appender: %w", err)
	}

	return &Writer{conn: conn, app: app}, nil
}

// Append adds a row. It isn't visible to queries until Flush.
func (w *Writer) Append(id, ts int64, raw string) error {
	err := w.app.AppendRow(id, ts, raw)
	if err != nil {
		return fmt.Errorf("append row: %w", err)
	}

	return nil
}

// Flush makes the appended rows visible to queries.
func (w *Writer) Flush() error {
	err := w.app.Flush()
	if err != nil {
		return fmt.Errorf("flush appender: %w", err)
	}

	return nil
}

// Close flushes the appender and releases the connection.
func (w *Writer) Close() error {
	appErr := w.app.Close()
	connErr := w.conn.Close()

	return errors.Join(appErr, connErr)
}
