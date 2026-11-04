package connector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// newTrailerRepo builds a repository, with real git, where one person reviews
// every storage change and never authors one, a co-author appears only in
// trailers, and Signed-off-by lines name a process participant who must not
// be credited.
func newTrailerRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, nil, "init", "-q", "-b", "main")
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	for i := range 5 {
		write("storage/engine.go", fmt.Sprintf("v%d\n", i))
		runGit(t, dir, nil, "add", "storage/engine.go")
		msg := fmt.Sprintf("storage: compaction pass %d\n\n"+
			"Co-authored-by: Pair Partner <pair@corp.com>\n"+
			"Reviewed-by: Vera Reviewer <vera@corp.com>\n"+
			"Reviewed-by: buildbot <bot@ci.example>\n"+
			"Signed-off-by: Process Person <process@corp.com>\n", i)
		runGit(t, dir, authorEnv("Main Author", "main@corp.com",
			fmt.Sprintf("2026-02-%02dT10:00:00Z", i+1)), "commit", "-q", "-m", msg)
	}
	return dir
}

// TestTrailerCredit verifies the review signal a repository carries offline
// reaches the graph: the reviewer who never authors a commit holds the
// subject, the co-author holds it directly, automation in trailers is
// skipped by name, and sign-offs credit nobody.
func TestTrailerCredit(t *testing.T) {
	t.Parallel()
	dir := newTrailerRepo(t)
	var log strings.Builder
	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 36500, Log: &log}).
		Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	byEmail := make(map[string]Record)
	for _, r := range recs {
		if r.Kind == KindPerson {
			byEmail[r.Email] = r
		}
	}

	vera, ok := byEmail["vera@corp.com"]
	if !ok {
		t.Fatalf("the reviewer is absent; people = %v", keysOf(byEmail))
	}
	if !slices.Contains(vera.Topics, "storage") {
		t.Errorf("vera topics = %v, want storage held through review alone", vera.Topics)
	}
	if slices.Contains(vera.DirectTopics, "storage") {
		t.Errorf("vera direct = %v; a reviewer did not change the files", vera.DirectTopics)
	}
	if vera.Name != "Vera Reviewer" {
		t.Errorf("vera name = %q", vera.Name)
	}

	pair, ok := byEmail["pair@corp.com"]
	if !ok {
		t.Fatal("the co-author is absent")
	}
	if !slices.Contains(pair.DirectTopics, "storage") {
		t.Errorf("pair direct = %v, want storage: a co-author changed the files", pair.DirectTopics)
	}

	if _, ok := byEmail["process@corp.com"]; ok {
		t.Error("a Signed-off-by line was credited; sign-off is process, not knowledge")
	}
	if _, ok := byEmail["bot@ci.example"]; ok {
		t.Error("an automation account was credited through a trailer")
	}
	if !strings.Contains(log.String(), "buildbot <bot@ci.example>") {
		t.Errorf("the skipped trailer bot is not named in the log:\n%s", log.String())
	}
}

// keysOf lists a record map's emails for a failure message.
func keysOf(m map[string]Record) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
