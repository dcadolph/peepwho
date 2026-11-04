package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIndexRefusesToReplaceWithNothing verifies a replacing run that read
// nothing leaves the existing index alone. Every connector reports an
// unreachable or unauthorized source as a skip rather than a failure, so an
// expired token yields zero records with no error, and replacing on that would
// erase a working graph while reporting success.
func TestIndexRefusesToReplaceWithNothing(t *testing.T) {
	dir := t.TempDir()
	csv := writeOrgCSV(t, dir)
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}
	indexPath := filepath.Join(dir, "index.json")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}

	// A source that yields no records at all, which is what every repo or
	// channel failing looks like from here.
	empty := filepath.Join(dir, "empty.csv")
	if err := os.WriteFile(empty, []byte("name,email,title,team,topics\n"), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", empty); !errors.Is(err, ErrNoRecords) {
		t.Fatalf("index with nothing to read = %v, want ErrNoRecords", err)
	}

	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the index was rewritten by a run that read nothing")
	}
	out, _, err := runCmd(t, "ask", "--data-dir", dir, "who owns retries")
	if err != nil || !strings.Contains(string(out), "jane@x.com") {
		t.Fatalf("index no longer answers after an empty run: %v\n%s", err, out)
	}
}

// TestIndexRefusesToReplaceWithMuchLess verifies a source that came back far
// smaller than last time leaves the index alone. A rate limit or a scope a
// token quietly lost makes a connector keep what it read and return no error,
// so a run that saw a fraction of a source would otherwise shrink the graph
// while reporting success.
func TestIndexRefusesToReplaceWithMuchLess(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "full.csv")
	content := "name,email,title,team,topics\n"
	for i := range 20 {
		content += fmt.Sprintf("Person %d,p%d@x.com,Engineer,Billing,retries\n", i, i)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", full); err != nil {
		t.Fatalf("index: %v", err)
	}
	indexPath := filepath.Join(dir, "index.json")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}

	// The same source, but only a couple of people came back this time.
	partial := filepath.Join(dir, "partial.csv")
	short := "name,email,title,team,topics\n" +
		"Person 0,p0@x.com,Engineer,Billing,retries\n" +
		"Person 1,p1@x.com,Engineer,Billing,retries\n"
	if err := os.WriteFile(partial, []byte(short), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	_, _, err = runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", partial)
	if !errors.Is(err, ErrShrunkSource) {
		t.Fatalf("truncated read = %v, want ErrShrunkSource", err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a truncated read rewrote the index")
	}

	// The same run is accepted when the shrink is deliberate.
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv",
		"--file", partial, "--allow-shrink"); err != nil {
		t.Fatalf("index with --allow-shrink: %v", err)
	}
	if raw, _ := os.ReadFile(indexPath); bytes.Equal(before, raw) {
		t.Error("--allow-shrink did not let the smaller read through")
	}
}
