package logstore

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

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
			rows, err := store.Newest(ctx, 10)
			require.NoError(t, err)
			require.Empty(t, rows)
		})
	})

	w, err := store.NewWriter(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	t.Run("Append", func(t *testing.T) {
		t.Run("is invisible until Flush", func(t *testing.T) {
			err := w.Append(1, 100, "first")
			require.NoError(t, err)

			rows, err := store.Newest(ctx, 10)
			require.NoError(t, err)
			require.Empty(t, rows)

			err = w.Flush()
			require.NoError(t, err)

			rows, err = store.Newest(ctx, 10)
			require.NoError(t, err)
			require.Equal(t, []Row{{ID: 1, TS: 100, Raw: "first"}}, rows)
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
					err := w.Append(r.ID, r.TS, r.Raw)
					require.NoError(t, err)
				}

				err := w.Flush()
				require.NoError(t, err)

				rows, err := store.Newest(ctx, 3)
				require.NoError(t, err)
				require.Equal(t, []Row{
					{ID: 3, TS: 200, Raw: "newest"},
					{ID: 2, TS: 100, Raw: "same ts"},
					{ID: 1, TS: 100, Raw: "first"},
				}, rows)
			},
		)
	})
}
