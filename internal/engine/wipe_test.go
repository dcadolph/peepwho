package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/peepwho/internal/util"
)

// writeWipeFiles creates each name under dir with placeholder content.
func writeWipeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// TestWipe verifies a wipe lists and removes every peepwho file with its lock
// and temporary siblings, keeps a file peepwho did not write along with the
// directory, and refuses a symlink without removing anything.
func TestWipe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Want      error
		Files     []string
		Symlink   string
		WantPlan  []string
		WantLeft  []string
		WantDirOK bool
	}{{ // Test 0: Every kept file, a lock, and a temporary write all go, then the directory.
		Files: []string{
			"index.json", "index.sources.json", "index.state.json", "episodes.json", "feedback.json",
			"facts.json", "changes.json", "refresh.json", "attest.key",
			"index.json" + util.LockSuffix, "changes.json.tmp-123",
		},
		WantPlan: []string{
			"index.json", "index.json" + util.LockSuffix, "index.sources.json", "index.state.json",
			"episodes.json", "feedback.json", "facts.json", "changes.json", "changes.json.tmp-123",
			"refresh.json", "attest.key",
		},
	}, { // Test 1: A file peepwho did not write survives, and so does the directory.
		Files:    []string{"index.json", "notes.txt"},
		WantPlan: []string{"index.json"}, WantLeft: []string{"notes.txt"}, WantDirOK: true,
	}, { // Test 2: A symlink at a kept path stops the plan and nothing is removed.
		Files: []string{"index.json"}, Symlink: "changes.json",
		Want: ErrBadArgs, WantLeft: []string{"changes.json", "index.json"}, WantDirOK: true,
	}, { // Test 3: An empty directory plans nothing and is removed.
		WantPlan: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "data")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			writeWipeFiles(t, dir, test.Files...)
			if test.Symlink != "" {
				target := filepath.Join(t.TempDir(), "elsewhere")
				writeWipeFiles(t, filepath.Dir(target), filepath.Base(target))
				if err := os.Symlink(target, filepath.Join(dir, test.Symlink)); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := PlanWipe(Dir(dir))
			if !errors.Is(err, test.Want) {
				t.Fatalf("PlanWipe error = %v, want %v", err, test.Want)
			}
			if err == nil {
				var got []string
				for _, f := range plan {
					got = append(got, filepath.Base(f))
				}
				if diff := cmp.Diff(test.WantPlan, got, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("plan mismatch (-want +got):\n%s", diff)
				}
				if err := Wipe(Dir(dir), plan); err != nil {
					t.Fatalf("Wipe: %v", err)
				}
			}
			entries, err := os.ReadDir(dir)
			if errors.Is(err, os.ErrNotExist) {
				if test.WantDirOK {
					t.Error("the directory was removed, want it kept")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !test.WantDirOK {
				t.Error("the directory was kept, want it removed")
			}
			var left []string
			for _, e := range entries {
				left = append(left, e.Name())
			}
			if diff := cmp.Diff(test.WantLeft, left, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("left mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
