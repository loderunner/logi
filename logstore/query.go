package logstore

import (
	"context"
	"fmt"
	"strings"
)

// Row is one stored log line.
type Row struct {
	ID  int64
	TS  int64
	Raw string
	// Values holds the value of each key passed to Newest, in the same order.
	// A key the row has no value for, or the store has never seen, has "".
	Values []string
}

// Newest returns up to limit rows, newest first, ordered by (ts, id)
// descending. Each Row has one value per entry in keys.
func (s *Store) Newest(
	ctx context.Context,
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

	query.WriteString(" FROM logs ORDER BY ts DESC, id DESC LIMIT ?")

	rows, err := s.db.QueryContext(ctx, query.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("query newest rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]Row, 0, limit)

	for rows.Next() {
		r := Row{Values: make([]string, len(keys))}

		dest := make([]any, 0, 3+len(keys))
		dest = append(dest, &r.ID, &r.TS, &r.Raw)

		for i := range r.Values {
			dest = append(dest, &r.Values[i])
		}

		err = rows.Scan(dest...)
		if err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		result = append(result, r)
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("read rows: %w", err)
	}

	return result, nil
}
