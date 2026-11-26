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
	// ID is the id that Append stored with the row.
	ID int64
	// TS is the time that Append stored with the row, in UTC.
	TS time.Time
	// Raw is the line as read.
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
	// Filter keeps only the rows it matches, before From and Move count
	// them. Nil keeps every row.
	Filter Filter
}

// Page is a window of rows, newest first.
type Page struct {
	// From is the top row of the page after the move, or nil when it is the
	// newest row.
	From *Key
	// Rows are the rows of the page, newest first. Each has one value per key
	// in the query.
	Rows []Row
}

// Page moves q.From by q.Move rows and returns up to q.Limit rows starting at
// the result.
//
// A move to older rows stops while the page is still full, so the page keeps
// q.Limit rows when the store has that many. A move to newer rows that
// reaches or passes the newest row returns a nil From.
//
// Only the rows that q.Filter keeps count, for the move and for the page.
// From needn't match the filter: the page starts at the first match at or
// below it, so the page is short when fewer than q.Limit matches are there,
// and moving newer from it reaches the newer matches.
//
// The page is read after the move, in a separate query, so rows that arrive
// in between can show in a page that follows the newest row.
func (s *Store) Page(ctx context.Context, q PageQuery) (Page, error) {
	// Render the filter once, so that a column added between the two queries
	// can't make them disagree.
	s.mu.RLock()
	filter := render(q.Filter, s.columns)
	s.mu.RUnlock()

	from, err := s.move(ctx, q.From, q.Move, q.Limit, filter)
	if err != nil {
		return Page{}, err
	}

	rows, err := s.rowsFrom(ctx, from, q.Limit, q.Keys, filter)
	if err != nil {
		return Page{}, err
	}

	return Page{From: from, Rows: rows}, nil
}

// move returns the key that is n rows from the key from, which is the newest
// row when nil. A positive n goes to older rows, a negative n to newer rows.
// A nil result is the newest row. Moving older stops when fewer than limit
// rows would remain. Only the rows that filter keeps count.
func (s *Store) move(
	ctx context.Context,
	from *Key,
	n int,
	limit int,
	filter condition,
) (*Key, error) {
	switch {
	case n > 0:
		return s.moveOlder(ctx, from, n, limit, filter)
	case n < 0 && from != nil:
		return s.moveNewer(ctx, *from, -n, filter)
	default:
		return from, nil
	}
}

// moveOlder moves from n rows to older rows, but not so far that fewer than
// limit rows remain at and below the result.
func (s *Store) moveOlder(
	ctx context.Context,
	from *Key,
	n int,
	limit int,
	filter condition,
) (*Key, error) {
	keys, err := s.keysAround(ctx, from, "<=", "DESC", n+limit, filter)
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
func (s *Store) moveNewer(
	ctx context.Context,
	from Key,
	n int,
	filter condition,
) (*Key, error) {
	// The keys start at the row just above from, so index n-1 is n rows newer.
	// A key at index n would be a row beyond the destination, which proves
	// the destination isn't the newest row.
	keys, err := s.keysAround(ctx, &from, ">", "ASC", n+1, filter)
	if err != nil {
		return nil, err
	}

	if len(keys) <= n {
		return nil, nil
	}

	return &keys[n-1], nil
}

// keysAround returns the keys of rows whose key compares to from with op, in
// the given order, up to limit of them. A nil from matches every row. Only
// the rows that filter keeps count.
func (s *Store) keysAround(
	ctx context.Context,
	from *Key,
	op string,
	order string,
	limit int,
	filter condition,
) ([]Key, error) {
	where, args := whereClause(keyFilter(from, op), filter)

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

// keyFilter returns a condition that keeps the rows whose (ts, id) compares to
// k with op, which is "<=" or ">". A nil k keeps every row.
func keyFilter(k *Key, op string) condition {
	if k == nil {
		return condition{}
	}

	ts := k.TS.UnixNano()
	strict := strings.TrimSuffix(op, "=")

	// Spelling out the comparison is about 3x faster than comparing the row
	// value, as in (ts, id) <= (?, ?).
	return condition{
		sql:  fmt.Sprintf("ts %s ? OR (ts = ? AND id %s ?)", strict, op),
		args: []any{ts, ts, k.ID},
	}
}

// rowsFrom returns up to limit rows, newest first, starting at from, which is
// the newest row when nil. Each Row has one value per entry in keys. Only the
// rows that filter keeps count.
func (s *Store) rowsFrom(
	ctx context.Context,
	from *Key,
	limit int,
	keys []string,
	filter condition,
) ([]Row, error) {
	var query strings.Builder

	query.WriteString("SELECT id, ts, raw")

	s.mu.RLock()

	for _, key := range keys {
		// A key with no column has no value in any row. The column name is
		// generated, so it needs no escaping beyond the quotes.
		name, ok := s.columns[key]
		if ok {
			fmt.Fprintf(&query, `, COALESCE("%s", '')`, name)
		} else {
			query.WriteString(", ''")
		}
	}

	s.mu.RUnlock()

	where, args := whereClause(keyFilter(from, "<="), filter)

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
