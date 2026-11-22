package logstore

import (
	"context"
	"fmt"
)

// Row is one stored log line.
type Row struct {
	ID  int64
	TS  int64
	Raw string
}

// Newest returns up to limit rows, newest first, ordered by (ts, id)
// descending.
func (s *Store) Newest(ctx context.Context, limit int) ([]Row, error) {
	rows, err := s.db.QueryContext(
		ctx,
		"SELECT id, ts, raw FROM logs ORDER BY ts DESC, id DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query newest rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]Row, 0, limit)

	for rows.Next() {
		var r Row

		err = rows.Scan(&r.ID, &r.TS, &r.Raw)
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
