package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dcadolph/whotoask/internal/util"
)

// PlanWipe lists the files present now that a wipe of dir removes: each file
// whotoask keeps, its lock, and any temporary file an interrupted atomic write
// left beside it. Anything at those paths that is not a regular file, such as
// a symlink, stops the plan, so a wipe never follows a link out of dir.
func PlanWipe(dir Dir) ([]string, error) {
	var files []string
	for _, path := range dir.Files() {
		candidates := []string{path, path + util.LockSuffix}
		temps, err := filepath.Glob(path + ".tmp-*")
		if err != nil {
			return nil, fmt.Errorf("wipe: list temporary files: %w", err)
		}
		candidates = append(candidates, temps...)
		for _, c := range candidates {
			info, err := os.Lstat(c)
			if err != nil {
				continue
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("%w: %s is not a regular file; remove it yourself", ErrBadArgs, c)
			}
			files = append(files, c)
		}
	}
	return files, nil
}

// Wipe removes files, then dir itself when nothing else is left in it. It
// removes everything it can before reporting, so one stuck file does not leave
// the rest behind.
func Wipe(dir Dir, files []string) error {
	var errs []error
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("wipe: remove %s: %w", f, err))
		}
	}
	// os.Remove refuses a directory that still holds anything, which is the
	// point: only a directory whotoask emptied goes.
	_ = os.Remove(string(dir)) //nolint:errcheck // A non-empty or missing directory stays as it is.
	return errors.Join(errs...)
}
