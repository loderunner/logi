package logstore

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// at returns the time n nanoseconds after the Unix epoch.
func at(n int64) time.Time {
	return time.Unix(0, n).UTC()
}

// newest returns the newest limit rows, with a value for each of keys.
func newest(
	ctx context.Context,
	store *Store,
	limit int,
	keys []string,
) ([]Row, error) {
	page, err := store.Page(ctx, PageQuery{Limit: limit, Keys: keys})

	return page.Rows, err
}

// openStore opens a Store whose temporary directories live under a fresh
// root, which it returns.
func openStore(t *testing.T) (*Store, string) {
	t.Helper()

	root := t.TempDir()
	t.Setenv("TMPDIR", root)

	store, err := Open(t.Context(), Options{MemoryLimit: "1GiB"})
	require.NoError(t, err)

	return store, root
}

func TestStore(t *testing.T) {
	t.Run("Open", func(t *testing.T) {
		t.Run("sets the memory limit", func(t *testing.T) {
			store, _ := openStore(t)
			t.Cleanup(func() { _ = store.Close() })

			var limit string

			err := store.db.QueryRowContext(
				t.Context(),
				"SELECT current_setting('memory_limit')",
			).Scan(&limit)
			require.NoError(t, err)
			require.Equal(t, "1.0 GiB", limit)
		})

		t.Run("creates a private directory", func(t *testing.T) {
			store, root := openStore(t)
			t.Cleanup(func() { _ = store.Close() })

			dirs, err := filepath.Glob(filepath.Join(root, "logi-*"))
			require.NoError(t, err)
			require.Len(t, dirs, 1)

			info, err := os.Stat(dirs[0])
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o700), info.Mode().Perm())

			var tempDir string

			err = store.db.QueryRowContext(
				t.Context(),
				"SELECT current_setting('temp_directory')",
			).Scan(&tempDir)
			require.NoError(t, err)
			require.Equal(t, dirs[0], tempDir)
		})

		t.Run("rejects an invalid memory limit", func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("TMPDIR", root)

			_, err := Open(t.Context(), Options{MemoryLimit: "lots"})
			require.Error(t, err)

			dirs, err := filepath.Glob(filepath.Join(root, "logi-*"))
			require.NoError(t, err)
			require.Empty(t, dirs)
		})
	})

	t.Run("Close", func(t *testing.T) {
		t.Run("removes the private directory", func(t *testing.T) {
			store, root := openStore(t)

			err := store.Close()
			require.NoError(t, err)

			dirs, err := filepath.Glob(filepath.Join(root, "logi-*"))
			require.NoError(t, err)
			require.Empty(t, dirs)
		})
	})
}

func TestCleanStale(t *testing.T) {
	t.Run("removes an unlocked directory", func(t *testing.T) {
		root := t.TempDir()

		dir, err := newPrivateDir(root)
		require.NoError(t, err)

		// Drop the lock without removing the directory, as a crash does.
		err = dir.lock.Close()
		require.NoError(t, err)

		err = cleanStale(root)
		require.NoError(t, err)
		require.NoDirExists(t, dir.path)
	})

	t.Run("keeps a locked directory", func(t *testing.T) {
		root := t.TempDir()

		dir, err := newPrivateDir(root)
		require.NoError(t, err)
		t.Cleanup(func() { _ = dir.Close() })

		err = cleanStale(root)
		require.NoError(t, err)
		require.DirExists(t, dir.path)
	})

	t.Run("keeps a directory held by another open file", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "logi-other")

		err := os.Mkdir(path, 0o700)
		require.NoError(t, err)

		lock, err := os.Create(filepath.Join(path, lockName))
		require.NoError(t, err)
		t.Cleanup(func() { _ = lock.Close() })

		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		require.NoError(t, err)

		err = cleanStale(root)
		require.NoError(t, err)
		require.DirExists(t, path)
	})

	t.Run("keeps a young directory with no lock file", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "logi-young")

		err := os.Mkdir(path, 0o700)
		require.NoError(t, err)

		err = cleanStale(root)
		require.NoError(t, err)
		require.DirExists(t, path)
	})

	t.Run("ignores other directories", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "other")

		err := os.Mkdir(path, 0o700)
		require.NoError(t, err)

		err = cleanStale(root)
		require.NoError(t, err)
		require.DirExists(t, path)
	})
}

func TestWriter(t *testing.T) {
	store, _ := openStore(t)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()

	t.Run("Page", func(t *testing.T) {
		t.Run("returns nothing on an empty store", func(t *testing.T) {
			rows, err := newest(ctx, store, 10, nil)
			require.NoError(t, err)
			require.Empty(t, rows)
		})
	})

	w, err := store.NewWriter(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	t.Run("Append", func(t *testing.T) {
		t.Run("is invisible until Flush", func(t *testing.T) {
			err := w.Append(1, at(100), "first", "first", map[string]string{
				"_raw": "first",
			})
			require.NoError(t, err)

			rows, err := newest(ctx, store, 10, []string{"_raw"})
			require.NoError(t, err)
			require.Empty(t, rows)

			keys, err := w.Flush()
			require.NoError(t, err)
			require.Equal(t, []string{"_raw"}, keys)

			rows, err = newest(ctx, store, 10, []string{"_raw"})
			require.NoError(t, err)
			require.Equal(t, []Row{
				{ID: 1, TS: at(100), Raw: "first", Values: []string{"first"}},
			}, rows)
		})
	})

	t.Run("Page", func(t *testing.T) {
		t.Run(
			"returns the newest rows first, up to the limit",
			func(t *testing.T) {
				for _, r := range []Row{
					{ID: 2, TS: at(100), Raw: "same ts"},
					{ID: 3, TS: at(200), Raw: "newest"},
					{ID: 4, TS: at(50), Raw: "late arrival"},
				} {
					err := w.Append(r.ID, r.TS, r.Raw, r.Raw, map[string]string{
						"_raw": r.Raw,
					})
					require.NoError(t, err)
				}

				keys, err := w.Flush()
				require.NoError(t, err)
				require.Empty(t, keys)

				rows, err := newest(ctx, store, 3, nil)
				require.NoError(t, err)
				require.Equal(t, []Row{
					{ID: 3, TS: at(200), Raw: "newest", Values: []string{}},
					{ID: 2, TS: at(100), Raw: "same ts", Values: []string{}},
					{ID: 1, TS: at(100), Raw: "first", Values: []string{}},
				}, rows)
			},
		)
	})
}

// newTestStore returns a Store and a Writer for it.
func newTestStore(t *testing.T) (*Store, *Writer) {
	t.Helper()

	store, _ := openStore(t)
	t.Cleanup(func() { _ = store.Close() })

	w, err := store.NewWriter(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	return store, w
}

func TestColumns(t *testing.T) {
	t.Run("a new key adds a column and keeps earlier rows", func(t *testing.T) {
		store, w := newTestStore(t)

		err := w.Append(1, at(10), "r1", "x", map[string]string{"a": "x"})
		require.NoError(t, err)

		// The new key forces the Appender to close and reopen, which must
		// store the row it holds.
		err = w.Append(
			2,
			at(20),
			"r2",
			"y z",
			map[string]string{"a": "y", "b": "z"},
		)
		require.NoError(t, err)

		keys, err := w.Flush()
		require.NoError(t, err)
		require.Equal(t, []string{"a", "b"}, keys)

		rows, err := newest(t.Context(), store, 10, []string{"a", "b"})
		require.NoError(t, err)
		require.Equal(t, []Row{
			{ID: 2, TS: at(20), Raw: "r2", Values: []string{"y", "z"}},
			{ID: 1, TS: at(10), Raw: "r1", Values: []string{"x", ""}},
		}, rows)
	})

	t.Run("Flush reports each key once", func(t *testing.T) {
		_, w := newTestStore(t)

		err := w.Append(1, at(1), "", "", map[string]string{"b": "1", "a": "1"})
		require.NoError(t, err)

		keys, err := w.Flush()
		require.NoError(t, err)
		require.Equal(t, []string{"a", "b"}, keys, "sorted within one row")

		err = w.Append(2, at(2), "", "", map[string]string{"a": "1", "c": "1"})
		require.NoError(t, err)

		keys, err = w.Flush()
		require.NoError(t, err)
		require.Equal(t, []string{"c"}, keys)

		keys, err = w.Flush()
		require.NoError(t, err)
		require.Empty(t, keys)
	})

	t.Run("Flush keeps keys added by an earlier flush", func(t *testing.T) {
		// Adding a column flushes on its own. The keys must still reach the
		// caller's next Flush.
		_, w := newTestStore(t)

		err := w.Append(1, at(1), "", "", map[string]string{"a": "1"})
		require.NoError(t, err)

		err = w.Append(2, at(2), "", "", map[string]string{"b": "1"})
		require.NoError(t, err)

		keys, err := w.Flush()
		require.NoError(t, err)
		require.Equal(t, []string{"a", "b"}, keys)
	})

	t.Run("keys that differ by case or match fixed columns are distinct",
		func(t *testing.T) {
			store, w := newTestStore(t)

			fields := map[string]string{
				"Level": "upper",
				"level": "lower",
				"id":    "my id",
				"ID":    "MY ID",
				"raw":   "my raw",
				"ts":    "my ts",
				"vals":  "my vals",
			}

			err := w.Append(7, at(70), "the raw", "the vals", fields)
			require.NoError(t, err)

			_, err = w.Flush()
			require.NoError(t, err)

			keys := []string{"Level", "level", "id", "ID", "raw", "ts", "vals"}

			rows, err := newest(t.Context(), store, 1, keys)
			require.NoError(t, err)
			require.Equal(t, []Row{
				{
					ID:  7,
					TS:  at(70),
					Raw: "the raw",
					Values: []string{
						"upper",
						"lower",
						"my id",
						"MY ID",
						"my raw",
						"my ts",
						"my vals",
					},
				},
			}, rows)
		})

	t.Run("stores vals for search", func(t *testing.T) {
		store, w := newTestStore(t)

		err := w.Append(
			1,
			at(1),
			"raw",
			"needle in a haystack",
			map[string]string{"a": "x"},
		)
		require.NoError(t, err)

		_, err = w.Flush()
		require.NoError(t, err)

		var vals string

		err = store.db.QueryRowContext(
			t.Context(),
			"SELECT vals FROM logs WHERE id = 1",
		).Scan(&vals)
		require.NoError(t, err)
		require.Equal(t, "needle in a haystack", vals)
	})

	t.Run("an unknown key has empty values", func(t *testing.T) {
		store, w := newTestStore(t)

		err := w.Append(1, at(1), "r", "", map[string]string{"a": "1"})
		require.NoError(t, err)

		_, err = w.Flush()
		require.NoError(t, err)

		rows, err := newest(t.Context(), store, 1, []string{"nope", "a"})
		require.NoError(t, err)
		require.Equal(t, []string{"", "1"}, rows[0].Values)
	})

	t.Run("replaces invalid UTF-8", func(t *testing.T) {
		store, w := newTestStore(t)

		err := w.Append(1, at(1), "a\xffb", "c\xffd", map[string]string{
			"k": "e\xfff",
		})
		require.NoError(t, err)

		_, err = w.Flush()
		require.NoError(t, err)

		rows, err := newest(t.Context(), store, 1, []string{"k"})
		require.NoError(t, err)
		require.Equal(t, "a\ufffdb", rows[0].Raw)
		require.Equal(t, []string{"e\ufffdf"}, rows[0].Values)

		var vals string

		err = store.db.QueryRowContext(
			t.Context(),
			"SELECT vals FROM logs WHERE id = 1",
		).Scan(&vals)
		require.NoError(t, err)
		require.Equal(t, "c\ufffdd", vals)
	})

	t.Run("stores special characters", func(t *testing.T) {
		store, w := newTestStore(t)

		value := "quote \" ; DROP TABLE logs; -- \n\t日本"

		err := w.Append(
			1,
			at(1),
			"r",
			value,
			map[string]string{`k"; --`: value},
		)
		require.NoError(t, err)

		_, err = w.Flush()
		require.NoError(t, err)

		rows, err := newest(t.Context(), store, 1, []string{`k"; --`})
		require.NoError(t, err)
		require.Equal(t, []string{value}, rows[0].Values)
	})

	t.Run("queries run while keys are added", func(t *testing.T) {
		store, w := newTestStore(t)

		var (
			done    = make(chan struct{})
			queried sync.WaitGroup
			known   atomic.Pointer[[]string]
		)

		queried.Add(1)

		go func() {
			defer queried.Done()

			for {
				select {
				case <-done:
					return
				default:
				}

				var keys []string

				p := known.Load()
				if p != nil {
					keys = *p
				}

				_, err := newest(t.Context(), store, 5, keys)
				assert.NoError(t, err)
			}
		}()

		var keys []string

		for i := range 50 {
			key := "key" + strconv.Itoa(i)

			err := w.Append(
				int64(i),
				at(int64(i)),
				"r",
				"",
				map[string]string{key: "v"},
			)
			require.NoError(t, err)

			added, err := w.Flush()
			require.NoError(t, err)

			keys = append(keys, added...)

			// The reader must not share the slice that keeps growing.
			published := slices.Clone(keys)
			known.Store(&published)
		}

		close(done)
		queried.Wait()

		rows, err := newest(t.Context(), store, 1, keys)
		require.NoError(t, err)
		require.Equal(t, "v", rows[0].Values[49])
	})
}

func TestTimestamps(t *testing.T) {
	t.Run("a row round-trips its time", func(t *testing.T) {
		store, w := newTestStore(t)

		ts := time.Date(2026, 3, 4, 5, 6, 7, 123_456_789, time.UTC)

		err := w.Append(1, ts, "r", "", map[string]string{"_raw": "r"})
		require.NoError(t, err)

		_, err = w.Flush()
		require.NoError(t, err)

		rows, err := newest(t.Context(), store, 1, nil)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.True(t, ts.Equal(rows[0].TS), "got %v", rows[0].TS)
		require.Equal(t, time.UTC, rows[0].TS.Location())
	})

	t.Run(
		"a time in another zone is stored as the same instant",
		func(t *testing.T) {
			store, w := newTestStore(t)

			zone := time.FixedZone("UTC+2", 2*60*60)
			ts := time.Date(2026, 3, 4, 7, 6, 7, 0, zone)

			err := w.Append(1, ts, "r", "", map[string]string{"_raw": "r"})
			require.NoError(t, err)

			_, err = w.Flush()
			require.NoError(t, err)

			rows, err := newest(t.Context(), store, 1, nil)
			require.NoError(t, err)
			require.True(t, ts.Equal(rows[0].TS))
		},
	)

	t.Run("Page orders by time, then id", func(t *testing.T) {
		store, w := newTestStore(t)

		base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

		// Out of order, with a tie between ids 3 and 4.
		for id, ts := range []time.Time{
			base.Add(2 * time.Second),
			base,
			base.Add(time.Second),
			base.Add(time.Second),
		} {
			err := w.Append(
				int64(id+1),
				ts,
				"",
				"",
				map[string]string{"_raw": "x"},
			)
			require.NoError(t, err)
		}

		_, err := w.Flush()
		require.NoError(t, err)

		rows, err := newest(t.Context(), store, 10, nil)
		require.NoError(t, err)

		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}

		require.Equal(t, []int64{1, 4, 3, 2}, ids)
	})

	t.Run("Restamp", func(t *testing.T) {
		// appendRows appends rows 1 to n, at 10ns apart, with the key "k".
		appendRows := func(t *testing.T, w *Writer, n int64) {
			t.Helper()

			for id := int64(1); id <= n; id++ {
				err := w.Append(
					id,
					at(id*10),
					"",
					"",
					map[string]string{"k": "v"},
				)
				require.NoError(t, err)
			}
		}

		t.Run(
			"updates rows that are appended but not flushed",
			func(t *testing.T) {
				store, w := newTestStore(t)
				appendRows(t, w, 3)

				err := w.Restamp(3, at(99))
				require.NoError(t, err)

				rows, err := newest(t.Context(), store, 10, nil)
				require.NoError(t, err)
				require.Equal(t, []Row{
					{ID: 2, TS: at(99), Values: []string{}},
					{ID: 1, TS: at(99), Values: []string{}},
					{ID: 3, TS: at(30), Values: []string{}},
				}, rows)
			},
		)

		t.Run("updates rows that are already flushed", func(t *testing.T) {
			store, w := newTestStore(t)
			appendRows(t, w, 2)

			_, err := w.Flush()
			require.NoError(t, err)

			err = w.Append(3, at(30), "", "", map[string]string{"k": "v"})
			require.NoError(t, err)

			err = w.Restamp(3, at(5))
			require.NoError(t, err)

			rows, err := newest(t.Context(), store, 10, nil)
			require.NoError(t, err)
			require.Equal(t, []Row{
				{ID: 3, TS: at(30), Values: []string{}},
				{ID: 2, TS: at(5), Values: []string{}},
				{ID: 1, TS: at(5), Values: []string{}},
			}, rows)
		})

		t.Run("restamping row 1 changes nothing", func(t *testing.T) {
			store, w := newTestStore(t)
			appendRows(t, w, 2)

			err := w.Restamp(1, at(99))
			require.NoError(t, err)

			rows, err := newest(t.Context(), store, 10, nil)
			require.NoError(t, err)
			require.Equal(t, []Row{
				{ID: 2, TS: at(20), Values: []string{}},
				{ID: 1, TS: at(10), Values: []string{}},
			}, rows)
		})

		t.Run("leaves the added keys for the next Flush", func(t *testing.T) {
			_, w := newTestStore(t)
			appendRows(t, w, 2)

			err := w.Restamp(2, at(99))
			require.NoError(t, err)

			keys, err := w.Flush()
			require.NoError(t, err)
			require.Equal(t, []string{"k"}, keys)
		})

		t.Run("keeps the Writer usable", func(t *testing.T) {
			store, w := newTestStore(t)
			appendRows(t, w, 2)

			err := w.Restamp(2, at(99))
			require.NoError(t, err)

			// A new key closes and reopens the Appender after the UPDATE.
			err = w.Append(3, at(100), "", "", map[string]string{"j": "w"})
			require.NoError(t, err)

			_, err = w.Flush()
			require.NoError(t, err)

			rows, err := newest(t.Context(), store, 10, []string{"j"})
			require.NoError(t, err)
			require.Equal(t, []Row{
				{ID: 3, TS: at(100), Values: []string{"w"}},
				{ID: 1, TS: at(99), Values: []string{""}},
				{ID: 2, TS: at(20), Values: []string{""}},
			}, rows)
		})
	})
}

// storeRows appends the rows with the given ids to w, each with the time
// id*10 nanoseconds after the epoch, and flushes.
func storeRows(t *testing.T, w *Writer, ids ...int64) {
	t.Helper()

	for _, id := range ids {
		storeRow(t, w, id, at(id*10))
	}

	_, err := w.Flush()
	require.NoError(t, err)
}

// storeRow appends a row to w and flushes.
func storeRow(t *testing.T, w *Writer, id int64, ts time.Time) {
	t.Helper()

	err := w.Append(id, ts, "r", "", map[string]string{"_raw": "r"})
	require.NoError(t, err)

	_, err = w.Flush()
	require.NoError(t, err)
}

// idRange returns the ids from through to, counting down if from > to.
func idRange(from, to int64) []int64 {
	var ids []int64

	step := int64(1)
	if from > to {
		step = -1
	}

	for id := from; id != to+step; id += step {
		ids = append(ids, id)
	}

	return ids
}

// rowIDs returns the ids of rows.
func rowIDs(rows []Row) []int64 {
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}

	return ids
}

// keyOf returns the key of the row stored by storeRows with the given id.
func keyOf(id int64) *Key {
	return &Key{TS: at(id * 10), ID: id}
}

func TestPage(t *testing.T) {
	ctx := t.Context()

	// twenty returns a store with rows 1 to 20, so id 20 is the newest.
	twenty := func(t *testing.T) (*Store, *Writer) {
		t.Helper()

		store, w := newTestStore(t)
		storeRows(t, w, idRange(1, 20)...)

		return store, w
	}

	t.Run("an empty store has an empty page", func(t *testing.T) {
		store, _ := newTestStore(t)

		for _, move := range []int{0, 5, -5} {
			page, err := store.Page(ctx, PageQuery{Move: move, Limit: 5})
			require.NoError(t, err)
			require.Nil(t, page.From)
			require.Empty(t, page.Rows)
		}
	})

	t.Run("starts at the newest row without From", func(t *testing.T) {
		store, _ := twenty(t)

		page, err := store.Page(
			ctx,
			PageQuery{Limit: 5, Keys: []string{"_raw"}},
		)
		require.NoError(t, err)
		require.Nil(t, page.From)
		require.Equal(t, idRange(20, 16), rowIDs(page.Rows))
		require.Equal(t, []string{"r"}, page.Rows[0].Values)
		require.Equal(t, *keyOf(20), page.Rows[0].Key())
	})

	t.Run("starts at From and keeps it without a move", func(t *testing.T) {
		store, _ := twenty(t)

		page, err := store.Page(ctx, PageQuery{From: keyOf(10), Limit: 5})
		require.NoError(t, err)
		require.Equal(t, keyOf(10), page.From)
		require.Equal(t, idRange(10, 6), rowIDs(page.Rows))
	})

	t.Run("rows with equal times order by id", func(t *testing.T) {
		store, w := newTestStore(t)

		for id := int64(1); id <= 4; id++ {
			storeRow(t, w, id, at(100))
		}

		from := &Key{TS: at(100), ID: 3}

		page, err := store.Page(ctx, PageQuery{From: from, Limit: 2})
		require.NoError(t, err)
		require.Equal(t, []int64{3, 2}, rowIDs(page.Rows))

		// Row 3 is newer than row 2, though they share a time.
		page, err = store.Page(ctx, PageQuery{
			From:  &Key{TS: at(100), ID: 2},
			Move:  -1,
			Limit: 2,
		})
		require.NoError(t, err)
		require.Equal(t, from, page.From)

		page, err = store.Page(ctx, PageQuery{From: from, Move: 1, Limit: 2})
		require.NoError(t, err)
		require.Equal(t, &Key{TS: at(100), ID: 2}, page.From)
		require.Equal(t, []int64{2, 1}, rowIDs(page.Rows))
	})

	t.Run("a page from a key leaves out newer rows added later",
		func(t *testing.T) {
			store, w := twenty(t)

			storeRows(t, w, 21, 22)

			page, err := store.Page(ctx, PageQuery{From: keyOf(20), Limit: 5})
			require.NoError(t, err)
			require.Equal(t, idRange(20, 16), rowIDs(page.Rows))

			page, err = store.Page(ctx, PageQuery{Limit: 5})
			require.NoError(t, err)
			require.Equal(t, idRange(22, 18), rowIDs(page.Rows))
		})

	t.Run("a late row older than the top shows below it", func(t *testing.T) {
		store, w := newTestStore(t)
		storeRows(t, w, idRange(1, 10)...)

		// Row 11 arrives after row 10 but is older than rows 4 and up.
		storeRow(t, w, 11, at(35))

		page, err := store.Page(ctx, PageQuery{From: keyOf(8), Limit: 6})
		require.NoError(t, err)
		require.Equal(t, keyOf(8), page.From)
		require.Equal(t, []int64{8, 7, 6, 5, 4, 11}, rowIDs(page.Rows))
	})

	t.Run("Move to older rows", func(t *testing.T) {
		t.Run("from the newest row", func(t *testing.T) {
			store, _ := twenty(t)

			page, err := store.Page(ctx, PageQuery{Move: 3, Limit: 5})
			require.NoError(t, err)
			require.Equal(t, keyOf(17), page.From)
			require.Equal(t, idRange(17, 13), rowIDs(page.Rows))
		})

		t.Run("from a key", func(t *testing.T) {
			store, _ := twenty(t)

			page, err := store.Page(ctx, PageQuery{
				From:  keyOf(15),
				Move:  4,
				Limit: 5,
			})
			require.NoError(t, err)
			require.Equal(t, keyOf(11), page.From)
			require.Equal(t, idRange(11, 7), rowIDs(page.Rows))
		})

		t.Run("stops while the page is full", func(t *testing.T) {
			store, _ := twenty(t)

			page, err := store.Page(ctx, PageQuery{Move: 100, Limit: 5})
			require.NoError(t, err)
			require.Equal(t, keyOf(5), page.From)
			require.Equal(t, idRange(5, 1), rowIDs(page.Rows))

			// From a key, too.
			page, err = store.Page(ctx, PageQuery{
				From:  keyOf(10),
				Move:  10,
				Limit: 5,
			})
			require.NoError(t, err)
			require.Equal(t, keyOf(5), page.From)
		})

		t.Run("keeps From at the end", func(t *testing.T) {
			store, _ := twenty(t)

			page, err := store.Page(ctx, PageQuery{
				From:  keyOf(5),
				Move:  1,
				Limit: 5,
			})
			require.NoError(t, err)
			require.Equal(t, keyOf(5), page.From)
			require.Equal(t, idRange(5, 1), rowIDs(page.Rows))
		})

		t.Run("stays on the newest row of a short table", func(t *testing.T) {
			store, w := newTestStore(t)
			storeRows(t, w, 1, 2, 3)

			page, err := store.Page(ctx, PageQuery{Move: 2, Limit: 5})
			require.NoError(t, err)
			require.Nil(t, page.From)
			require.Equal(t, []int64{3, 2, 1}, rowIDs(page.Rows))
		})
	})

	t.Run("Move to newer rows", func(t *testing.T) {
		t.Run(
			"stays on the newest row from the newest row",
			func(t *testing.T) {
				store, _ := twenty(t)

				page, err := store.Page(ctx, PageQuery{Move: -3, Limit: 5})
				require.NoError(t, err)
				require.Nil(t, page.From)
				require.Equal(t, idRange(20, 16), rowIDs(page.Rows))
			},
		)

		t.Run("partway returns the key it lands on", func(t *testing.T) {
			store, _ := twenty(t)

			for move, want := range map[int]int64{-1: 11, -3: 13, -9: 19} {
				page, err := store.Page(ctx, PageQuery{
					From:  keyOf(10),
					Move:  move,
					Limit: 5,
				})
				require.NoError(t, err)
				require.Equal(t, keyOf(want), page.From, "move %d", move)
				require.Equal(t, idRange(want, want-4), rowIDs(page.Rows))
			}
		})

		t.Run("landing on the newest row follows", func(t *testing.T) {
			store, _ := twenty(t)

			page, err := store.Page(ctx, PageQuery{
				From:  keyOf(10),
				Move:  -10,
				Limit: 5,
			})
			require.NoError(t, err)
			require.Nil(t, page.From)
			require.Equal(t, idRange(20, 16), rowIDs(page.Rows))
		})

		t.Run("passing the newest row follows", func(t *testing.T) {
			store, _ := twenty(t)

			page, err := store.Page(ctx, PageQuery{
				From:  keyOf(10),
				Move:  -50,
				Limit: 5,
			})
			require.NoError(t, err)
			require.Nil(t, page.From)
			require.Equal(t, idRange(20, 16), rowIDs(page.Rows))
		})
	})

	t.Run("moves by a page and back", func(t *testing.T) {
		store, _ := twenty(t)

		down, err := store.Page(ctx, PageQuery{Move: 5, Limit: 5})
		require.NoError(t, err)
		require.Equal(t, keyOf(15), down.From)
		require.Equal(t, idRange(15, 11), rowIDs(down.Rows))

		down, err = store.Page(
			ctx,
			PageQuery{From: down.From, Move: 5, Limit: 5},
		)
		require.NoError(t, err)
		require.Equal(t, idRange(10, 6), rowIDs(down.Rows))

		up, err := store.Page(
			ctx,
			PageQuery{From: down.From, Move: -5, Limit: 5},
		)
		require.NoError(t, err)
		require.Equal(t, keyOf(15), up.From)

		up, err = store.Page(ctx, PageQuery{From: up.From, Move: -5, Limit: 5})
		require.NoError(t, err)
		require.Nil(t, up.From, "the last page up reaches the newest row")
		require.Equal(t, idRange(20, 16), rowIDs(up.Rows))
	})
}
