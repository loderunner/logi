package logstore

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	// dirPattern is the name pattern of every directory that newPrivateDir
	// creates, which is also what cleanStale looks for.
	dirPattern = "logi-*"
	// lockName is the lock file in each private directory.
	lockName = "lock"

	// orphanAge is how old a directory with no lock file must be before
	// cleanStale removes it. A younger one may belong to a logi that has
	// created the directory but not yet its lock.
	orphanAge = time.Minute
)

// privateDir is a temporary directory that belongs to one running logi. The
// store spills to it when data exceeds the memory limit.
//
// The directory holds a lock file with an exclusive flock for as long as the
// process lives. The kernel releases the lock when the process exits, even
// when it crashes, so a lock that anyone can take marks a directory left
// behind. A PID file can't tell that, because PIDs get reused.
type privateDir struct {
	path string
	lock *os.File
}

// newPrivateDir creates a directory under root, with mode 0700, and locks it.
// Close removes it.
func newPrivateDir(root string) (*privateDir, error) {
	path, err := os.MkdirTemp(root, dirPattern)
	if err != nil {
		return nil, fmt.Errorf("create temp directory: %w", err)
	}

	lock, err := os.OpenFile(
		filepath.Join(path, lockName),
		os.O_RDWR|os.O_CREATE,
		0o600,
	)
	if err != nil {
		_ = os.RemoveAll(path)
		return nil, fmt.Errorf("create lock file: %w", err)
	}

	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		_ = lock.Close()
		_ = os.RemoveAll(path)
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}

	return &privateDir{path: path, lock: lock}, nil
}

// Close removes the directory and releases the lock.
func (d *privateDir) Close() error {
	err := os.RemoveAll(d.path)
	if err != nil {
		_ = d.lock.Close()
		return fmt.Errorf("remove %s: %w", d.path, err)
	}

	err = d.lock.Close()
	if err != nil {
		return fmt.Errorf("release lock: %w", err)
	}

	return nil
}

// cleanStale removes the directories under root that no running logi holds.
// It skips a directory it can't inspect, such as one owned by another user,
// because it can't be ours.
func cleanStale(root string) error {
	paths, err := filepath.Glob(filepath.Join(root, dirPattern))
	if err != nil {
		return fmt.Errorf("list temp directories: %w", err)
	}

	for _, path := range paths {
		removeIfStale(path)
	}

	return nil
}

// removeIfStale removes the directory at path unless a running logi holds its
// lock. A directory with no lock file is left alone until it is older than
// orphanAge.
func removeIfStale(path string) {
	lock, err := os.OpenFile(filepath.Join(path, lockName), os.O_RDWR, 0)
	if os.IsNotExist(err) {
		info, statErr := os.Stat(path)
		if statErr == nil && time.Since(info.ModTime()) > orphanAge {
			_ = os.RemoveAll(path)
		}

		return
	}
	if err != nil {
		// Not ours to inspect, such as a directory that another user owns.
		return
	}
	defer func() { _ = lock.Close() }()

	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		// Held by a running logi.
		return
	}

	// Remove the directory while holding the lock, so that two logi starting
	// together don't both clean it.
	_ = os.RemoveAll(path)
}
