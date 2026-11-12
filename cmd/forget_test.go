package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/engine"
	"github.com/dcadolph/whotoask/internal/feedback"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// TestForgetVotes checks that a purge clears the votes cast on every purged
// identity and leaves every other vote, never lets a blank identity act as a
// filter that matches everything, and never creates a feedback file.
func TestForgetVotes(t *testing.T) {
	t.Parallel()
	seeded := []feedback.Entry{
		{Query: "billing", Person: "bob@corp.com", Vote: feedback.Helpful},
		{Query: "billing", Person: "SLACK:U2", Vote: feedback.NotHelpful},
		{Query: "billing", Person: "zed@corp.com", Vote: feedback.Helpful},
		{Query: "billing", Channel: "payments", Vote: feedback.Helpful},
	}
	everyone := []string{"bob@corp.com", "SLACK:U2", "zed@corp.com", "#payments"}
	tests := []struct {
		PurgeIDs  []model.ID
		WantKept  []string
		WantCount int
		Seed      bool
		WantFile  bool
	}{{ // Test 0: Votes on every purged identity go, matched regardless of case.
		Seed: true, PurgeIDs: []model.ID{"bob@corp.com", "slack:u2"},
		WantCount: 2, WantKept: []string{"zed@corp.com", "#payments"}, WantFile: true,
	}, { // Test 1: A blank identity never clears the votes on anybody else.
		Seed: true, PurgeIDs: []model.ID{"", "  "},
		WantCount: 0, WantKept: everyone, WantFile: true,
	}, { // Test 2: An identity nobody voted on removes nothing.
		Seed: true, PurgeIDs: []model.ID{"amy@corp.com"},
		WantCount: 0, WantKept: everyone, WantFile: true,
	}, { // Test 3: With no feedback file, none is created.
		Seed: false, PurgeIDs: []model.ID{"bob@corp.com"},
		WantCount: 0, WantKept: nil, WantFile: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			opts := &options{dataDir: t.TempDir()}
			if test.Seed {
				store, err := feedback.Load(opts.feedbackPath(), nil)
				if err != nil {
					t.Fatalf("load: %v", err)
				}
				for _, e := range seeded {
					if err := store.Add(e); err != nil {
						t.Fatalf("add: %v", err)
					}
				}
			}
			got, err := forgetVotes(opts, test.PurgeIDs)
			if err != nil {
				t.Fatalf("forgetVotes: %v", err)
			}
			if got != test.WantCount {
				t.Errorf("removed = %d, want %d", got, test.WantCount)
			}
			_, statErr := os.Stat(opts.feedbackPath())
			if gotFile := statErr == nil; gotFile != test.WantFile {
				t.Fatalf("feedback file exists = %v, want %v", gotFile, test.WantFile)
			}
			store, err := feedback.Load(opts.feedbackPath(), nil)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			var kept []string
			for _, e := range store.All() {
				if e.Channel != "" {
					kept = append(kept, "#"+e.Channel)
					continue
				}
				kept = append(kept, e.Person)
			}
			if diff := cmp.Diff(test.WantKept, kept, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("kept votes mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

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

	// Votes on bob and on alice. The vote on bob stores his identifier, so the
	// file scan below proves it went, and the vote on alice must stay.
	for _, voted := range []string{"bob@corp.com", "alice@corp.com"} {
		if _, stderr, err := runCmd(t, "feedback", "record", "--data-dir", dir,
			"--person", voted, "--helpful", "consumer lag dashboards"); err != nil {
			t.Fatalf("feedback record %s: %v\n%s", voted, err, stderr)
		}
	}

	_, stderr, err := runCmd(t, "forget", "--data-dir", dir, "--yes", "bob@corp.com")
	if err != nil {
		t.Fatalf("forget: %v\n%s", err, stderr)
	}
	if !strings.Contains(string(stderr), "1 votes removed") {
		t.Errorf("forget did not report the removed vote:\n%s", stderr)
	}
	votesOut, _, err := runCmd(t, "feedback", "list", "--data-dir", dir, "--json")
	if err != nil {
		t.Fatalf("feedback list after forget: %v", err)
	}
	if !strings.Contains(string(votesOut), "alice@corp.com") {
		t.Errorf("the vote on alice went with bob's:\n%s", votesOut)
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
