// Package logstore keeps log lines in an in-memory DuckDB database. It is the
// only package that writes SQL.
package logstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/duckdb/duckdb-go/v2"
)

// Options configures a Store.
type Options struct {
	// MemoryLimit is DuckDB's memory_limit, such as "1GB". Data over the limit
	// spills to a private temporary directory.
	MemoryLimit string
}

// Store is the log database. It is safe for concurrent use, except that it
// has one Writer at a time.
type Store struct {
	db  *sql.DB
	dir *privateDir
}

// Open removes the temporary directories that crashed runs left behind,
// creates a private one for this run, and opens an empty database.
func Open(ctx context.Context, opts Options) (*Store, error) {
	root := os.TempDir()

	err := cleanStale(root)
	if err != nil {
		return nil, fmt.Errorf("clean stale directories: %w", err)
	}

	dir, err := newPrivateDir(root)
	if err != nil {
		return nil, err
	}

	connector, err := duckdb.NewConnector(
		"",
		func(execer driver.ExecerContext) error {
			return configure(ctx, execer, opts.MemoryLimit, dir.path)
		},
	)
	if err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("open duckdb: %w", err)
	}

	db := sql.OpenDB(connector)

	_, err = db.ExecContext(
		ctx,
		"CREATE TABLE logs(id BIGINT, ts BIGINT, raw VARCHAR)",
	)
	if err != nil {
		_ = db.Close()
		_ = dir.Close()

		return nil, fmt.Errorf("create logs table: %w", err)
	}

	return &Store{db: db, dir: dir}, nil
}

// Close closes the database and removes the private directory. Close the
// Writer first.
func (s *Store) Close() error {
	dbErr := s.db.Close()
	dirErr := s.dir.Close()

	return errors.Join(dbErr, dirErr)
}

// configure runs on each new connection.
func configure(
	ctx context.Context,
	execer driver.ExecerContext,
	memoryLimit, tempDir string,
) error {
	settings := []struct{ name, value string }{
		{"memory_limit", quote(memoryLimit)},
		{"temp_directory", quote(tempDir)},
	}

	for _, s := range settings {
		stmt := fmt.Sprintf("SET %s = %s", s.name, s.value)

		_, err := execer.ExecContext(ctx, stmt, nil)
		if err != nil {
			return fmt.Errorf("set %s: %w", s.name, err)
		}
	}

	return nil
}

// quote returns s as a SQL string literal.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
