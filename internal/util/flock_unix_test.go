package util

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestLockFileSerializesHolders proves the lock actually excludes: a second
// taker blocks until the first releases, so two writers can never interleave
// on the state file the lock guards.
func TestLockFileSerializesHolders(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.lock")

	release, err := LockFile(path)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	var second atomic.Bool
	acquired := make(chan struct{})
	go func() {
		rel, err := LockFile(path)
		if err != nil {
			t.Errorf("second lock: %v", err)
			close(acquired)
			return
		}
		second.Store(true)
		close(acquired)
		rel()
	}()

	// The second taker must still be waiting while the first holds.
	time.Sleep(50 * time.Millisecond)
	if second.Load() {
		t.Fatal("second lock acquired while the first was held")
	}

	release()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second lock never acquired after release")
	}
	if !second.Load() {
		t.Fatal("second taker finished without holding the lock")
	}
}

// TestLockFileReacquireAfterRelease proves release really lets go: the same
// goroutine can take the lock again immediately.
func TestLockFileReacquireAfterRelease(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.lock")
	release, err := LockFile(path)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	release()
	again, err := LockFile(path)
	if err != nil {
		t.Fatalf("relock: %v", err)
	}
	again()
}

// TestLockFileCreatesItsDirectory covers the first run of a fresh install.
//
// A lock taken around a store's first write runs before anything has created
// the data directory. Refusing there broke the primary command on every new
// install: the first index reported "open lock: no such file or directory" and
// wrote nothing.
func TestLockFileCreatesItsDirectory(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "fresh", "install")
	release, err := LockFile(filepath.Join(dir, "index.json.lock"))
	if err != nil {
		t.Fatalf("lock in a directory that does not exist yet: %v", err)
	}
	defer release()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat the created directory: %v", err)
	}
	// The lock sits beside the stores, so it must not widen their permissions.
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("directory mode = %v, want 0700: it holds indexed work data", perm)
	}
}

// TestLockFileBadPath covers the open failure that remains: a path whose parent
// is a file can never be made a directory, so it is an error rather than a
// panic or a silent no-op lock.
func TestLockFileBadPath(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LockFile(filepath.Join(blocker, "state.lock")); err == nil {
		t.Fatal("no error for a lock path underneath a file")
	}
}
