//go:build !unix

package util

// LockSuffix names the sibling advisory-lock file that guards writes to a store.
const LockSuffix = ".lock"

// LockFile is a no-op where flock is unavailable: single-process use stays
// correct and cross-process writes fall back to last-writer-wins.
func LockFile(string) (func(), error) {
	return func() {}, nil
}
