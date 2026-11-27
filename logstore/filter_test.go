package logstore

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// storeFields appends one row per element of fields to w, with ids 1, 2, …
// and the time id*10 nanoseconds after the epoch, and flushes.
func storeFields(t *testing.T, w *Writer, fields ...map[string]string) {
	t.Helper()

	for i, f := range fields {
		id := int64(i + 1)

		err := w.Append(id, at(id*10), "r", "", f)
		require.NoError(t, err)
	}

	_, err := w.Flush()
	require.NoError(t, err)
}

// filtered returns the ids of the newest rows that f keeps.
func filtered(t *testing.T, store *Store, f Filter) []int64 {
	t.Helper()

	page, err := store.Page(t.Context(), PageQuery{Limit: 100, Filter: f})
	require.NoError(t, err)

	return rowIDs(page.Rows)
}

func TestMatch(t *testing.T) {
	t.Run("keeps rows with exactly the value, case-sensitively",
		func(t *testing.T) {
			store, w := newTestStore(t)
			storeFields(t, w,
				map[string]string{"level": "error"},
				map[string]string{"level": "info"},
				map[string]string{"level": "ERROR"},
				map[string]string{"level": "error "},
				map[string]string{"level": "errors"},
				map[string]string{"level": "error"},
			)

			require.Equal(t, []int64{6, 1}, filtered(t, store,
				Match{Key: "level", Value: "error"}))
			require.Equal(t, []int64{3}, filtered(t, store,
				Match{Key: "level", Value: "ERROR"}))
			require.Empty(t, filtered(t, store,
				Match{Key: "level", Value: "err"}))
			require.Empty(t, filtered(t, store,
				Match{Key: "level", Value: "%"}))
		})

	t.Run("matches a dotted key by its exact name", func(t *testing.T) {
		store, w := newTestStore(t)
		storeFields(t, w,
			map[string]string{"http.method": "GET"},
			map[string]string{"http.method": "POST", "http": "GET"},
		)

		require.Equal(t, []int64{1}, filtered(t, store,
			Match{Key: "http.method", Value: "GET"}))
		require.Equal(t, []int64{2}, filtered(t, store,
			Match{Key: "http", Value: "GET"}))
	})

	t.Run("an unseen key matches nothing, unless the value is empty",
		func(t *testing.T) {
			store, w := newTestStore(t)
			storeFields(t, w,
				map[string]string{"level": "error"},
				map[string]string{"level": "info"},
			)

			require.Empty(t, filtered(t, store,
				Match{Key: "service", Value: "billing"}))
			require.Equal(t, []int64{2, 1}, filtered(t, store,
				Match{Key: "service", Value: ""}))
		})

	t.Run("a key starts matching once its column exists", func(t *testing.T) {
		store, w := newTestStore(t)
		storeFields(t, w, map[string]string{"level": "error"})

		f := Match{Key: "service", Value: "billing"}
		require.Empty(t, filtered(t, store, f))

		err := w.Append(
			2,
			at(20),
			"r",
			"",
			map[string]string{"service": "billing"},
		)
		require.NoError(t, err)

		_, err = w.Flush()
		require.NoError(t, err)

		require.Equal(t, []int64{2}, filtered(t, store, f))
		require.Equal(t, []int64{1}, filtered(t, store,
			Match{Key: "service", Value: ""}))
	})

	t.Run("an empty value matches a missing key and an empty string",
		func(t *testing.T) {
			store, w := newTestStore(t)
			storeFields(t, w,
				map[string]string{"level": "error"},
				map[string]string{"other": "x"},
				map[string]string{"level": ""},
				map[string]string{"level": "info"},
			)

			require.Equal(t, []int64{3, 2}, filtered(t, store,
				Match{Key: "level", Value: ""}))
			require.Equal(t, []int64{4}, filtered(t, store,
				Match{Key: "level", Value: "info"}))
		})

	t.Run("quotes in the key and the value are plain text", func(t *testing.T) {
		store, w := newTestStore(t)
		storeFields(t, w,
			map[string]string{`a"b`: "o'brien"},
			map[string]string{`a"b`: "x", "a": "o'brien"},
			map[string]string{`a"b`: `' OR '1'='1`},
		)

		require.Equal(t, []int64{1}, filtered(t, store,
			Match{Key: `a"b`, Value: "o'brien"}))
		require.Equal(t, []int64{3}, filtered(t, store,
			Match{Key: `a"b`, Value: `' OR '1'='1`}))
		require.Equal(t, []int64{2}, filtered(t, store,
			Match{Key: "a", Value: "o'brien"}))
		require.Empty(t, filtered(t, store,
			Match{Key: `a"b" OR TRUE --`, Value: "x"}))
	})
}

func TestPageFilter(t *testing.T) {
	ctx := t.Context()

	// threes returns a store with rows 1 to 20, where the rows whose id is a
	// multiple of 3 have kind "m", and the filter that keeps them. The rows
	// kept, newest first, are 18, 15, 12, 9, 6 and 3.
	threes := func(t *testing.T) (*Store, Filter) {
		t.Helper()

		store, w := newTestStore(t)

		fields := make([]map[string]string, 20)
		for i := range fields {
			kind := "o"
			if (i+1)%3 == 0 {
				kind = "m"
			}

			fields[i] = map[string]string{"kind": kind}
		}

		storeFields(t, w, fields...)

		return store, Match{Key: "kind", Value: "m"}
	}

	t.Run("a nil filter keeps every row", func(t *testing.T) {
		store, _ := threes(t)

		page, err := store.Page(ctx, PageQuery{
			From:   keyOf(15),
			Move:   2,
			Limit:  3,
			Filter: nil,
		})
		require.NoError(t, err)
		require.Equal(t, keyOf(13), page.From)
		require.Equal(t, idRange(13, 11), rowIDs(page.Rows))
	})

	t.Run("the page has only matching rows", func(t *testing.T) {
		store, f := threes(t)

		page, err := store.Page(ctx, PageQuery{
			Limit:  4,
			Keys:   []string{"kind"},
			Filter: f,
		})
		require.NoError(t, err)
		require.Nil(t, page.From)
		require.Equal(t, []int64{18, 15, 12, 9}, rowIDs(page.Rows))

		for _, r := range page.Rows {
			require.Equal(t, []string{"m"}, r.Values)
		}
	})

	t.Run("a page from a key stays below it", func(t *testing.T) {
		store, f := threes(t)

		// The key's condition holds an OR, so it needs its parentheses next to
		// the filter's, or the rows above From would leak in.
		page, err := store.Page(ctx, PageQuery{
			From:   keyOf(10),
			Limit:  5,
			Filter: f,
		})
		require.NoError(t, err)
		require.Equal(t, keyOf(10), page.From)
		require.Equal(t, []int64{9, 6, 3}, rowIDs(page.Rows))
	})

	t.Run("moving older counts only matching rows", func(t *testing.T) {
		store, f := threes(t)

		// From doesn't match. The matches at and below it are 18, 15, 12, 9.
		page, err := store.Page(ctx, PageQuery{
			From:   keyOf(20),
			Move:   2,
			Limit:  2,
			Filter: f,
		})
		require.NoError(t, err)
		require.Equal(t, keyOf(12), page.From)
		require.Equal(t, []int64{12, 9}, rowIDs(page.Rows))
	})

	t.Run("moving older stops while the page is full", func(t *testing.T) {
		store, f := threes(t)

		page, err := store.Page(ctx, PageQuery{
			From:   keyOf(20),
			Move:   50,
			Limit:  4,
			Filter: f,
		})
		require.NoError(t, err)
		require.Equal(t, keyOf(12), page.From)
		require.Equal(t, []int64{12, 9, 6, 3}, rowIDs(page.Rows))
	})

	t.Run("a short page below the anchor", func(t *testing.T) {
		store, f := threes(t)

		// Only 6 and 3 are at or below row 7, so the page is short, and
		// moving older has nowhere to go.
		page, err := store.Page(ctx, PageQuery{
			From:   keyOf(7),
			Limit:  5,
			Filter: f,
		})
		require.NoError(t, err)
		require.Equal(t, keyOf(7), page.From)
		require.Equal(t, []int64{6, 3}, rowIDs(page.Rows))

		page, err = store.Page(ctx, PageQuery{
			From:   keyOf(7),
			Move:   1,
			Limit:  5,
			Filter: f,
		})
		require.NoError(t, err)
		require.Equal(t, keyOf(7), page.From)
		require.Equal(t, []int64{6, 3}, rowIDs(page.Rows))
	})

	t.Run("moving newer from a row that doesn't match", func(t *testing.T) {
		store, f := threes(t)

		// The matches above row 7 are 9, 12, 15, 18, so one row newer is 9.
		page, err := store.Page(ctx, PageQuery{
			From:   keyOf(7),
			Move:   -1,
			Limit:  5,
			Filter: f,
		})
		require.NoError(t, err)
		require.Equal(t, keyOf(9), page.From)
		require.Equal(t, []int64{9, 6, 3}, rowIDs(page.Rows))

		page, err = store.Page(ctx, PageQuery{
			From:   keyOf(7),
			Move:   -3,
			Limit:  5,
			Filter: f,
		})
		require.NoError(t, err)
		require.Equal(t, keyOf(15), page.From)
		require.Equal(t, []int64{15, 12, 9, 6, 3}, rowIDs(page.Rows))
	})

	t.Run("moving newer past the newest match follows new rows",
		func(t *testing.T) {
			store, f := threes(t)

			page, err := store.Page(ctx, PageQuery{
				From:   keyOf(7),
				Move:   -4,
				Limit:  3,
				Filter: f,
			})
			require.NoError(t, err)
			require.Nil(t, page.From)
			require.Equal(t, []int64{18, 15, 12}, rowIDs(page.Rows))
		})

	t.Run("no matches gives an empty page", func(t *testing.T) {
		store, _ := threes(t)
		f := Match{Key: "kind", Value: "none"}

		for _, move := range []int{0, 3, -3} {
			page, err := store.Page(ctx, PageQuery{
				From:   keyOf(10),
				Move:   move,
				Limit:  5,
				Filter: f,
			})
			require.NoError(t, err)
			require.Empty(t, page.Rows)
		}
	})
}

func TestWhereClause(t *testing.T) {
	t.Run("no conditions has no clause", func(t *testing.T) {
		sql, args := whereClause()
		require.Empty(t, sql)
		require.Empty(t, args)

		sql, args = whereClause(condition{}, condition{})
		require.Empty(t, sql)
		require.Empty(t, args)
	})

	t.Run("parenthesizes and joins non-empty conditions", func(t *testing.T) {
		sql, args := whereClause(
			condition{sql: "a OR b", args: []any{1}},
			condition{},
			condition{sql: "c = ?", args: []any{2, 3}},
		)
		require.Equal(t, " WHERE (a OR b) AND (c = ?)", sql)
		require.Equal(t, []any{1, 2, 3}, args)
	})
}
