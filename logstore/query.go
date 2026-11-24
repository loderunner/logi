package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Row is one stored log line.
type Row struct {
	ID  int64
	TS  time.Time
	Raw string
	// Values holds the value of each key passed to Page, in the same order.
	// A key the row has no value for, or the store has never seen, has "".
	Values []string
}

// Key is a row's place in the (ts, id) order.
type Key struct {
	TS time.Time
	ID int64
}

// Key returns the row's place in the order.
func (r Row) Key() Key {
	return Key{TS: r.TS, ID: r.ID}
}

// PageQuery asks for a page of rows, newest first, ordered by (ts, id)
// descending.
type PageQuery struct {
	// From is the top row of the page. Nil means the newest row, so the page
	// follows new rows.
	From *Key
	// Move shifts From before the page is read: a positive Move goes to older
	// rows, a negative one to newer rows.
	Move int
	// Limit is the number of rows to return.
	Limit int
	// Keys are the keys to read a value for in each row.
	Keys []string
}

// Page is a window of rows, newest first.
type Page struct {
	// From is the top row of the page after the move, or nil when it is the
	// newest row.
	From *Key
	// Rows has one value per key in the query, in each row.
	Rows []Row
}

// Page moves q.From by q.Move rows and returns up to q.Limit rows starting at
// the result.
//
// A move to older rows stops while the page is still full, so the oldest row
// can't rise above the bottom line. A move to newer rows that reaches or
// passes the newest row returns a nil From.
//
// The page is read after the move, in a separate query, so rows that arrive
// in between can show in a page that follows the newest row.
func (s *Store) Page(ctx context.Context, q PageQuery) (Page, error) {
	from, err := s.move(ctx, q.From, q.Move, q.Limit)
	if err != nil {
		return Page{}, err
	}

	rows, err := s.rowsFrom(ctx, from, q.Limit, q.Keys)
	if err != nil {
		return Page{}, err
	}

	return Page{From: from, Rows: rows}, nil
}

// move returns the key that is n rows from the key from, which is the newest
// row when nil. A positive n goes to older rows, a negative n to newer rows.
// A nil result is the newest row. Moving older stops when fewer than limit
// rows would remain.
func (s *Store) move(
	ctx context.Context,
	from *Key,
	n int,
	limit int,
) (*Key, error) {
	switch {
	case n > 0:
		return s.moveOlder(ctx, from, n, limit)
	case n < 0 && from != nil:
		return s.moveNewer(ctx, *from, -n)
	default:
		return from, nil
	}
}

// moveOlder moves from n rows to older rows, but not so far that fewer than
// limit rows are left from the result down.
func (s *Store) moveOlder(
	ctx context.Context,
	from *Key,
	n int,
	limit int,
) (*Key, error) {
	keys, err := s.keysAround(ctx, from, "<=", "DESC", n+limit)
	if err != nil {
		return nil, err
	}

	// The keys start at from, so index n is n rows older.
	idx := min(n, max(len(keys)-limit, 0))
	if idx == 0 {
		return from, nil
	}

	return &keys[idx], nil
}

// moveNewer moves from n rows to newer rows. It returns nil when that reaches
// the newest row, or goes past it.
func (s *Store) moveNewer(ctx context.Context, from Key, n int) (*Key, error) {
	// The keys start at the row just above from, so index n-1 is n rows newer.
	// A key at index n would be a row beyond the destination, which proves
	// the destination isn't the newest row.
	keys, err := s.keysAround(ctx, &from, ">", "ASC", n+1)
	if err != nil {
		return nil, err
	}

	if len(keys) <= n {
		return nil, nil
	}

	return &keys[n-1], nil
}

// keysAround returns the keys of rows whose key compares to from with op, in
// the given order, up to limit of them. A nil from matches every row.
func (s *Store) keysAround(
	ctx context.Context,
	from *Key,
	op string,
	order string,
	limit int,
) ([]Key, error) {
	where, args := keyFilter(from, op)

	query := fmt.Sprintf(
		"SELECT id, ts FROM logs%s ORDER BY ts %s, id %s LIMIT ?",
		where,
		order,
		order,
	)

	rows, err := s.db.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("query keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var keys []Key

	for rows.Next() {
		var (
			k  Key
			ts int64
		)

		err = rows.Scan(&k.ID, &ts)
		if err != nil {
			return nil, fmt.Errorf("scan key: %w", err)
		}

		k.TS = time.Unix(0, ts).UTC()
		keys = append(keys, k)
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("read keys: %w", err)
	}

	return keys, nil
}

// keyFilter returns a WHERE clause, with a leading space, that keeps the rows
// whose (ts, id) compares to k with op, which is "<=" or ">". It returns
// nothing for a nil k, which matches every row.
func keyFilter(k *Key, op string) (string, []any) {
	if k == nil {
		return "", nil
	}

	ts := k.TS.UnixNano()
	strict := strings.TrimSuffix(op, "=")

	// Spelling out the comparison instead of a row value (ts, id) <= (?, ?)
	// is ~3x faster
	return fmt.Sprintf(
			" WHERE ts %s ? OR (ts = ? AND id %s ?)",
			strict,
			op,
		),
		[]any{ts, ts, k.ID}
}

// rowsFrom returns up to limit rows, newest first, starting at from, which is
// the newest row when nil. Each Row has one value per entry in keys.
func (s *Store) rowsFrom(
	ctx context.Context,
	from *Key,
	limit int,
	keys []string,
) ([]Row, error) {
	var query strings.Builder

	query.WriteString("SELECT id, ts, raw")

	s.mu.RLock()

	for _, key := range keys {
		name, ok := s.columns[key]
		if ok {
			fmt.Fprintf(&query, `, COALESCE("%s", '')`, name)
		} else {
			query.WriteString(", ''")
		}
	}

	s.mu.RUnlock()

	where, args := keyFilter(from, "<=")

	query.WriteString(" FROM logs")
	query.WriteString(where)
	query.WriteString(" ORDER BY ts DESC, id DESC LIMIT ?")

	rows, err := s.db.QueryContext(ctx, query.String(), append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("query rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanRows(rows, limit, len(keys))
}

// scanRows reads rows of id, ts, raw and nKeys values into Rows.
func scanRows(rows *sql.Rows, limit int, nKeys int) ([]Row, error) {
	result := make([]Row, 0, limit)

	for rows.Next() {
		r := Row{Values: make([]string, nKeys)}

		dest := make([]any, 0, 3+nKeys)
		var ts int64

		dest = append(dest, &r.ID, &ts, &r.Raw)

		for i := range r.Values {
			dest = append(dest, &r.Values[i])
		}

		err := rows.Scan(dest...)
		if err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		r.TS = time.Unix(0, ts).UTC()
		result = append(result, r)
	}

	err := rows.Err()
	if err != nil {
		return nil, fmt.Errorf("read rows: %w", err)
	}

	return result, nil
}
