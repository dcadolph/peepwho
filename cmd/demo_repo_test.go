package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
)

// TestDemoRepoRefusesANonRepository verifies the demo says what is wrong
// rather than serving an empty page. A demo that comes up blank reads as a
// broken product, and the mistake it is most likely to be given is a path that
// is not a repository.
func TestDemoRepoRefusesANonRepository(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err := runCmd(t, "demo", "--repo", dir, "--open=false", "--addr", "127.0.0.1:0")
	if err == nil {
		t.Fatal("demo --repo on a directory with no history served instead of failing")
	}
	if !errors.Is(err, connector.ErrNoReadableRepo) || !strings.Contains(err.Error(), "could be read") {
		t.Errorf("error = %v, want it to say the path could not be read", err)
	}
}
