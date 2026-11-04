package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDirectoryLists verifies the directory command prints what is indexed,
// narrows to a section, and rejects an unknown one. It also closes the loop on
// the ask hint that points a confused user here.
func TestDirectoryLists(t *testing.T) {
	dir := t.TempDir()
	csv := writeOrgCSV(t, dir)
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}

	stdout, _, err := runCmd(t, "directory", "--data-dir", dir)
	if err != nil {
		t.Fatalf("directory: %v", err)
	}
	if !strings.Contains(string(stdout), `"people"`) {
		t.Errorf("directory output = %s, want a people section", stdout)
	}

	stdout, _, err = runCmd(t, "directory", "--data-dir", dir, "topics")
	if err != nil {
		t.Fatalf("directory topics: %v", err)
	}
	// Only the topics section, so the channels section key is absent (the topic
	// rows carry a people count of their own, which is not the people section).
	if !strings.Contains(string(stdout), `"topics"`) || strings.Contains(string(stdout), `"channels"`) {
		t.Errorf("directory topics = %s, want only the topics section", stdout)
	}

	if _, _, err := runCmd(t, "directory", "--data-dir", dir, "nope"); err == nil {
		t.Error("an unknown directory section did not error")
	}
}

// TestStatusReportsFreshnessAndCounts verifies status prints the counts, the
// per-source sizes, and a build time, so a user can tell what is indexed and how
// stale it is without re-indexing.
func TestStatusReportsFreshnessAndCounts(t *testing.T) {
	dir := t.TempDir()
	csv := writeOrgCSV(t, dir)
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}

	stdout, _, err := runCmd(t, "status", "--data-dir", dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{`"people"`, `"sources"`, `"org-csv"`, `"built_at"`} {
		if !strings.Contains(string(stdout), want) {
			t.Errorf("status output missing %s: %s", want, stdout)
		}
	}
}

// TestStatusCountsSubjectsApartFromMinedWords verifies the two are reported
// separately rather than summed into one "topics" figure.
//
// They are not the same thing and differ by orders of magnitude. A subject is
// something a source stated: a label, a component, a directory. A word is mined
// from prose so a question asked in somebody's own vocabulary still matches. On
// a real issue tracker the mined words outnumber the stated subjects four
// hundred to one, and a single total made the index look like it understood four
// hundred times more than it did.
func TestStatusCountsSubjectsApartFromMinedWords(t *testing.T) {
	dir := t.TempDir()
	csv := writeOrgCSV(t, dir)
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}
	stdout, _, err := runCmd(t, "status", "--data-dir", dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["subjects"]; !ok {
		t.Error(`status has no "subjects" count`)
	}
	if _, ok := got["words"]; !ok {
		t.Error(`status has no "words" count`)
	}
	if _, ok := got["topics"]; ok {
		t.Error(`status still reports a combined "topics" count, which conflates the two`)
	}
}
