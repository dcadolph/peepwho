//go:build unix

package util

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// LockSuffix names the sibling advisory-lock file that guards writes to a
// store. It is locked in place of the data file, whose atomic rename would
// otherwise detach a lock held on it.
const LockSuffix = ".lock"

// LockFile takes an exclusive advisory lock on path, creating it if needed, and
// returns a function that releases the lock and closes the file.
func LockFile(path string) (func(), error) {
	// The directory has to exist before the lock file can. A lock taken around
	// a store's first write runs before anything has created the data
	// directory, so opening the lock failed and the first index of a fresh
	// install reported "open lock: no such file or directory" and wrote
	// nothing. Same permissions the stores use, since this sits beside them.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("util: lock directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("util: open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("util: lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
