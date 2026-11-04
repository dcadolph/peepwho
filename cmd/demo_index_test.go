package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// savedDemo writes the simulated company to a fresh data directory, the way
// demo --save-index does, and returns the directory.
func savedDemo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "demo")
	if _, stderr, err := runCmd(t, "demo", "--save-index", dir); err != nil {
		t.Fatalf("demo --save-index: %v\n%s", err, stderr)
	}
	return dir
}

// TestDemoSaveIndex verifies demo --save-index writes an index and episodes
// that the rest of the commands read as a real data directory.
func TestDemoSaveIndex(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "demo")
	_, stderr, err := runCmd(t, "demo", "--save-index", dir)
	if err != nil {
		t.Fatalf("demo --save-index: %v\n%s", err, stderr)
	}
	for _, name := range []string{"index.json", "episodes.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("demo --save-index wrote no %s: %v", name, err)
		}
	}
	if !strings.Contains(string(stderr), "wrote the sample company to "+dir) {
		t.Errorf("stderr does not name the directory:\n%s", stderr)
	}
	out, _, err := runCmd(t, "status", "--data-dir", dir, "--json")
	if err != nil {
		t.Fatalf("status on the saved demo: %v", err)
	}
	if !strings.Contains(string(out), "people") {
		t.Errorf("status on the saved demo names no people: %s", out)
	}
}
