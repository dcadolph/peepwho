package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dcadolph/whotoask/internal/util"
)

// TestFirstRunIntoAMissingDataDir runs each command that takes a store lock
// against a data directory that does not exist yet, which is every fresh
// install. Taking a lock before a store's first write opens the lock file
// before anything has created the directory to hold it, and that failed with
// "open lock: no such file or directory" while writing nothing. A test given
// an already-created directory cannot see it, which is how it shipped.
func TestFirstRunIntoAMissingDataDir(t *testing.T) {
	t.Parallel()
	csv := filepath.Join(t.TempDir(), "people.csv")
	if err := os.WriteFile(csv,
		[]byte("name,email,title,team,topics\nJo,jo@x.com,Eng,Core,kafka\n"), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	tests := []struct {
		Name      string
		Args      []string
		WantFiles []string
	}{{ // Test 0: The index lock, which also covers the sources sidecar.
		Name: "index", Args: []string{"index", "--source", "org-csv", "--file", csv},
		WantFiles: []string{"index.json", "index.sources.json", "refresh.json"},
	}, { // Test 1: The facts lock, when a fact is the very first thing stored.
		Name:      "fact record",
		Args:      []string{"fact", "record", "team:pay", "escalates_to", "person:jane@corp.com"},
		WantFiles: []string{"facts.json"},
	}, { // Test 2: The feedback lock, likewise first.
		Name:      "feedback record",
		Args:      []string{"feedback", "record", "who owns billing", "--person", "jane@corp.com", "--helpful"},
		WantFiles: []string{"feedback.json"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			// Two levels below the temp directory, so neither the data
			// directory nor its parent exists when the lock is taken.
			dir := filepath.Join(t.TempDir(), "never-existed", ".whotoask")
			args := append(append([]string{}, test.Args...), "--data-dir", dir)
			if _, stderr, err := runCmd(t, args...); err != nil {
				t.Fatalf("%s into a missing data dir: %v\n%s", test.Name, err, stderr)
			}
			for _, name := range test.WantFiles {
				if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
					t.Errorf("%s did not write %s: %v", test.Name, name, err)
				}
			}
		})
	}
}

// TestForgetLeavesNothingBehindWithNoIndex checks a purge with nothing to
// purge from creates no data directory. Taking the index lock creates one, so
// the check for an index has to come first, or running forget once on a
// machine that never indexed anything leaves a directory and a lock file
// behind.
func TestForgetLeavesNothingBehindWithNoIndex(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "never-existed", ".whotoask")
	_, _, err := runCmd(t, "forget", "--data-dir", dir, "--yes", "jane@corp.com")
	if err == nil {
		t.Fatal("forget with no index: want error, got nil")
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		entries, _ := os.ReadDir(dir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("forget created %s on a machine with no index: %v", dir, names)
	}
}

// TestIndexRecordsWaitsForLock verifies an index run serializes on the index
// lock file. Without it, a scheduled refresh overlapping a manual merge each
// did a plain load-merge-save over the same three files and one side's records
// vanished while both reported success.
func TestIndexRecordsWaitsForLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	csv := filepath.Join(dir, "people.csv")
	if err := os.WriteFile(csv,
		[]byte("name,email,title,team,topics\nJo,jo@x.com,Eng,Core,kafka\n"), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	unlock, err := util.LockFile(filepath.Join(dir, "index.json") + util.LockSuffix)
	if err != nil {
		t.Fatalf("take lock: %v", err)
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv)
		done <- err
	}()

	// The run must not finish while the lock is held. The wait is generous in
	// the passing direction: a run that ignores the lock finishes this small
	// index in well under the window, so completion here means the lock is not
	// being taken.
	select {
	case err := <-done:
		t.Fatalf("index finished while another process held the lock (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}

	locked = false
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("index after unlock: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("index never finished after the lock was released")
	}
	if _, err := os.Stat(filepath.Join(dir, "index.json")); err != nil {
		t.Fatalf("index file after locked run: %v", err)
	}
}
