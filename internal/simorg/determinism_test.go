package simorg

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestIndexBytesDeterministic holds the product claim at its strongest: the
// same company, ingested twice through the real pipeline, saves to an
// identical index file byte for byte. Anything nondeterministic between a
// fetch and the file, map iteration in a connector, goroutine completion
// order, unsorted output, lands here as a diff.
func TestIndexBytesDeterministic(t *testing.T) {
	t.Parallel()
	spec := Spec{Seed: 7}
	org := Generate(spec)
	defer org.Close()
	save := func(run int) []byte {
		t.Helper()
		dir := t.TempDir()
		built, err := BuildFrom(org, dir, spec)
		if err != nil {
			t.Fatalf("BuildFrom run %d: %v", run, err)
		}
		path := filepath.Join(dir, "index.json")
		if err := built.Index.Save(path); err != nil {
			t.Fatalf("Save run %d: %v", run, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read run %d: %v", run, err)
		}
		return data
	}
	first, second := save(1), save(2)
	// The build stamp is provenance, not map content: it records when this
	// file was written and is the one field allowed to differ.
	stamp := regexp.MustCompile(`"built_at":"[^"]*"`)
	a := stamp.ReplaceAllString(string(first), `"built_at":""`)
	b := stamp.ReplaceAllString(string(second), `"built_at":""`)
	if a != b {
		i := 0
		for i < len(a) && i < len(b) && a[i] == b[i] {
			i++
		}
		lo, hi := max(0, i-60), min(len(a), i+60)
		t.Errorf("two ingests of one company saved different bytes at offset %d:\n%q\n%q",
			i, a[lo:hi], b[lo:min(len(b), i+60)])
	}
}
