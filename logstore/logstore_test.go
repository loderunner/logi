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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	t.Run("Newest", func(t *testing.T) {
		t.Run("returns nothing on an empty store", func(t *testing.T) {
			rows, err := store.Newest(ctx, 10, nil)
			require.NoError(t, err)
			require.Empty(t, rows)
		})
	})

	w, err := store.NewWriter(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	t.Run("Append", func(t *testing.T) {
		t.Run("is invisible until Flush", func(t *testing.T) {
			err := w.Append(1, 100, "first", "first", map[string]string{
				"_raw": "first",
			})
			require.NoError(t, err)

			rows, err := store.Newest(ctx, 10, []string{"_raw"})
			require.NoError(t, err)
			require.Empty(t, rows)

			keys, err := w.Flush()
			require.NoError(t, err)
			require.Equal(t, []string{"_raw"}, keys)

			rows, err = store.Newest(ctx, 10, []string{"_raw"})
			require.NoError(t, err)
			require.Equal(t, []Row{
				{ID: 1, TS: 100, Raw: "first", Values: []string{"first"}},
			}, rows)
		})
	})

	t.Run("Newest", func(t *testing.T) {
		t.Run(
			"returns the newest rows first, up to the limit",
			func(t *testing.T) {
				for _, r := range []Row{
					{ID: 2, TS: 100, Raw: "same ts"},
					{ID: 3, TS: 200, Raw: "newest"},
					{ID: 4, TS: 50, Raw: "late arrival"},
				} {
					err := w.Append(r.ID, r.TS, r.Raw, r.Raw, map[string]string{
						"_raw": r.Raw,
					})
					require.NoError(t, err)
				}

				keys, err := w.Flush()
				require.NoError(t, err)
				require.Empty(t, keys)

				rows, err := store.Newest(ctx, 3, nil)
				require.NoError(t, err)
				require.Equal(t, []Row{
					{ID: 3, TS: 200, Raw: "newest", Values: []string{}},
					{ID: 2, TS: 100, Raw: "same ts", Values: []string{}},
					{ID: 1, TS: 100, Raw: "first", Values: []string{}},
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

		err := w.Append(1, 10, "r1", "x", map[string]string{"a": "x"})
		require.NoError(t, err)

		// The new key forces the Appender to close and reopen, which must
		// store the row it holds.
		err = w.Append(
			2,
			20,
			"r2",
			"y z",
			map[string]string{"a": "y", "b": "z"},
		)
		require.NoError(t, err)

		keys, err := w.Flush()
		require.NoError(t, err)
		require.Equal(t, []string{"a", "b"}, keys)

		rows, err := store.Newest(t.Context(), 10, []string{"a", "b"})
		require.NoError(t, err)
		require.Equal(t, []Row{
			{ID: 2, TS: 20, Raw: "r2", Values: []string{"y", "z"}},
			{ID: 1, TS: 10, Raw: "r1", Values: []string{"x", ""}},
		}, rows)
	})

	t.Run("Flush reports each key once", func(t *testing.T) {
		_, w := newTestStore(t)

		err := w.Append(1, 1, "", "", map[string]string{"b": "1", "a": "1"})
		require.NoError(t, err)

		keys, err := w.Flush()
		require.NoError(t, err)
		require.Equal(t, []string{"a", "b"}, keys, "sorted within one row")

		err = w.Append(2, 2, "", "", map[string]string{"a": "1", "c": "1"})
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

		err := w.Append(1, 1, "", "", map[string]string{"a": "1"})
		require.NoError(t, err)

		err = w.Append(2, 2, "", "", map[string]string{"b": "1"})
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

			err := w.Append(7, 70, "the raw", "the vals", fields)
			require.NoError(t, err)

			_, err = w.Flush()
			require.NoError(t, err)

			keys := []string{"Level", "level", "id", "ID", "raw", "ts", "vals"}

			rows, err := store.Newest(t.Context(), 1, keys)
			require.NoError(t, err)
			require.Equal(t, []Row{
				{
					ID:  7,
					TS:  70,
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
			1,
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

		err := w.Append(1, 1, "r", "", map[string]string{"a": "1"})
		require.NoError(t, err)

		_, err = w.Flush()
		require.NoError(t, err)

		rows, err := store.Newest(t.Context(), 1, []string{"nope", "a"})
		require.NoError(t, err)
		require.Equal(t, []string{"", "1"}, rows[0].Values)
	})

	t.Run("replaces invalid UTF-8", func(t *testing.T) {
		store, w := newTestStore(t)

		err := w.Append(1, 1, "a\xffb", "c\xffd", map[string]string{
			"k": "e\xfff",
		})
		require.NoError(t, err)

		_, err = w.Flush()
		require.NoError(t, err)

		rows, err := store.Newest(t.Context(), 1, []string{"k"})
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

		err := w.Append(1, 1, "r", value, map[string]string{`k"; --`: value})
		require.NoError(t, err)

		_, err = w.Flush()
		require.NoError(t, err)

		rows, err := store.Newest(t.Context(), 1, []string{`k"; --`})
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

				_, err := store.Newest(t.Context(), 5, keys)
				assert.NoError(t, err)
			}
		}()

		var keys []string

		for i := range 50 {
			key := "key" + strconv.Itoa(i)

			err := w.Append(
				int64(i),
				int64(i),
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

		rows, err := store.Newest(t.Context(), 1, keys)
		require.NoError(t, err)
		require.Equal(t, "v", rows[0].Values[49])
	})
}
