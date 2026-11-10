package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/engine"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// TestForgetPurgesPerson indexes the Slack export fixture, purges one person,
// and then proves they are gone from every file whotoask keeps and every answer
// it gives, while everyone else survives.
func TestForgetPurgesPerson(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// A first index of someone else, so the Slack fold below is a change the
	// change log records with bob among the people who joined.
	csv := filepath.Join(t.TempDir(), "people.csv")
	rows := "name,email,title,team,topics\nZed Example,zed@corp.com,Eng,Core,billing\n"
	if err := os.WriteFile(csv, []byte(rows), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	if _, stderr, err := runCmd(t, "index", "--data-dir", dir,
		"--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index csv: %v\n%s", err, stderr)
	}
	if _, stderr, err := runCmd(t, "index", "--data-dir", dir,
		"--source", "slack-export", "--file", exportZip, "--merge",
		"--episodes", "--since-days", "36500"); err != nil {
		t.Fatalf("index: %v\n%s", err, stderr)
	}
	logged, err := os.ReadFile(filepath.Join(dir, "changes.json"))
	if err != nil {
		t.Fatalf("read change log: %v", err)
	}
	if !strings.Contains(string(logged), "Bob Okafor") {
		t.Fatalf("change log does not name bob before the purge:\n%s", logged)
	}

	// A recorded fact naming bob, which survives re-indexing by design and so
	// must be covered by the purge or it answers about him forever.
	if _, stderr, err := runCmd(t, "fact", "record", "--data-dir", dir,
		"team:pay", "escalates_to", "person:bob@corp.com", "--detail", "pager escalation"); err != nil {
		t.Fatalf("fact record: %v\n%s", err, stderr)
	}

	// Before: bob is findable.
	out, _, err := runCmd(t, "ask", "--data-dir", dir, "who knows about consumer lag dashboards")
	if err != nil {
		t.Fatalf("ask before: %v", err)
	}
	if !strings.Contains(string(out), "bob@corp.com") {
		t.Fatalf("bob not findable before the purge:\n%s", out)
	}

	if _, stderr, err := runCmd(t, "forget", "--data-dir", dir, "--yes", "bob@corp.com"); err != nil {
		t.Fatalf("forget: %v\n%s", err, stderr)
	}

	// The fact is gone from the store, not merely from one file scan.
	factsOut, _, err := runCmd(t, "fact", "list", "--data-dir", dir, "--json")
	if err != nil {
		t.Fatalf("fact list after forget: %v", err)
	}
	if strings.Contains(string(factsOut), "bob@corp.com") {
		t.Errorf("a recorded fact still names the purged person:\n%s", factsOut)
	}

	// Gone from every stored byte, name and email both.
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, trace := range []string{"bob@corp.com", "Bob Okafor"} {
			if strings.Contains(string(data), trace) {
				t.Errorf("%s still contains %q after forget", path, trace)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// The change log still parses and still names the people who remain.
	var entries []engine.ChangeEntry
	logged, err = os.ReadFile(filepath.Join(dir, "changes.json"))
	if err != nil {
		t.Fatalf("read change log after forget: %v", err)
	}
	if err := json.Unmarshal(logged, &entries); err != nil {
		t.Fatalf("change log after forget does not parse: %v\n%s", err, logged)
	}
	if len(entries) != 1 || len(entries[0].Changes.PeopleJoined) == 0 {
		t.Errorf("change log lost the people who remain:\n%s", logged)
	}

	// Gone from answers, while alice still stands.
	out, _, err = runCmd(t, "ask", "--data-dir", dir, "who knows about the kafka upgrade")
	if err != nil {
		t.Fatalf("ask after: %v", err)
	}
	var ans resolve.JSONAnswer
	if err := json.Unmarshal(out, &ans); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	for _, p := range ans.People {
		if p.Email == "bob@corp.com" {
			t.Error("bob still answers after being forgotten")
		}
	}
	found := false
	for _, p := range ans.People {
		if p.Email == "alice@corp.com" {
			found = true
		}
	}
	if !found {
		t.Error("alice vanished; the purge took more than the named person")
	}
}

// TestForgetAmbiguousName verifies purging by a name two people share is
// refused with both candidates listed, and that naming one by email still
// works. The old lookup returned an arbitrary namesake and forget purged it:
// a nondeterministically chosen person deleted with no warning.
func TestForgetAmbiguousName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	csv := filepath.Join(dir, "people.csv")
	rows := "name,email,title,team,topics\n" +
		"Jane Roe,jane@corp.com,Eng,Core,kafka\n" +
		"Jane Roe,jane.roe@other.com,PM,Growth,billing\n"
	if err := os.WriteFile(csv, []byte(rows), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}

	_, _, err := runCmd(t, "forget", "--data-dir", dir, "--yes", "Jane Roe")
	if err == nil {
		t.Fatal("forgetting an ambiguous name: want error, got nil")
		return
	}
	for _, id := range []string{"jane@corp.com", "jane.roe@other.com"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error does not name candidate %s: %v", id, err)
		}
	}

	// Both survive the refused purge.
	out, _, err := runCmd(t, "directory", "--data-dir", dir, "--json")
	if err != nil {
		t.Fatalf("directory: %v", err)
	}
	for _, id := range []string{"jane@corp.com", "jane.roe@other.com"} {
		if !strings.Contains(string(out), id) {
			t.Errorf("%s vanished after a refused purge:\n%s", id, out)
		}
	}

	// Named precisely, the purge goes through and takes only that person.
	if _, stderr, err := runCmd(t, "forget", "--data-dir", dir, "--yes", "jane@corp.com"); err != nil {
		t.Fatalf("forget by email: %v\n%s", err, stderr)
	}
	out, _, err = runCmd(t, "directory", "--data-dir", dir, "--json")
	if err != nil {
		t.Fatalf("directory after: %v", err)
	}
	if strings.Contains(string(out), "jane@corp.com") {
		t.Error("the named person survived the purge")
	}
	if !strings.Contains(string(out), "jane.roe@other.com") {
		t.Error("the namesake was purged too")
	}
}

// TestForgetUnknownPerson verifies purging a stranger is an error, not a
// silent no-op.
func TestForgetUnknownPerson(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	csv := filepath.Join(dir, "people.csv")
	if err := os.WriteFile(csv,
		[]byte("name,email,title,team,topics\nJo,jo@x.com,Eng,Core,kafka\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}
	if _, _, err := runCmd(t, "forget", "--data-dir", dir, "--yes", "nobody@x.com"); err == nil {
		t.Fatal("forgetting an unknown person: want error, got nil")
	}
}
