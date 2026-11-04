package connector

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// newTwoCommitRepo builds a repository with one commit per given author and
// path, in order, each at its stated time.
func newTwoCommitRepo(t *testing.T, commits []struct{ Email, Path, Date string }) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, nil, "init", "-q", "-b", "main")
	for _, c := range commits {
		writeRepoFile(t, dir, c.Path, "content\n")
		runGit(t, dir, nil, "add", c.Path)
		runGit(t, dir, authorEnv("Alice Doe", c.Email, c.Date), "commit", "-q", "-m", "work "+c.Path)
	}
	return dir
}

// TestGitNormalizesPlusTaggedEmails verifies the place tally keys on the
// canonical mailbox the identity graph uses: a plus-tagged commit address like
// alice+ci@corp.com used to stand beside alice@corp.com as a second holder in
// the systems file, splitting one person's work in two.
func TestGitNormalizesPlusTaggedEmails(t *testing.T) {
	t.Parallel()
	dir := newTwoCommitRepo(t, []struct{ Email, Path, Date string }{
		{"alice+ci@corp.com", "billing/impl.go", "2026-01-02T10:00:00Z"},
		{"alice@corp.com", "billing/retry.go", "2026-01-03T10:00:00Z"},
	})
	g := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 36500})
	recs, err := g.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var emails []string
	for _, r := range recs {
		if r.Kind == KindPerson {
			emails = append(emails, r.Email)
		}
	}
	if len(emails) != 1 || emails[0] != "alice@corp.com" {
		t.Errorf("person emails = %v, want one canonical alice@corp.com", emails)
	}
	holders := g.DirWork()["billing"]
	if len(holders) != 1 || holders["alice@corp.com"] != 2 {
		t.Errorf("billing holders = %v, want alice@corp.com holding both commits", holders)
	}
	if got := g.WorkTotals()["alice@corp.com"]; got != 2 {
		t.Errorf("work total = %v, want 2 under the canonical mailbox", got)
	}
	if _, ok := g.WorkTotals()["alice+ci@corp.com"]; ok {
		t.Error("the plus-tagged address survived as its own person")
	}
}

// TestGitTrailerEmailsNormalized verifies trailer credit lands on the same
// canonical mailbox commits do, so a plus-tagged Co-authored-by never mints a
// second holder.
func TestGitTrailerEmailsNormalized(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runGit(t, dir, nil, "init", "-q", "-b", "main")
	writeRepoFile(t, dir, "billing/impl.go", "content\n")
	runGit(t, dir, nil, "add", "billing/impl.go")
	runGit(t, dir, authorEnv("Alice Doe", "alice@corp.com", "2026-01-02T10:00:00Z"),
		"commit", "-q", "-m", "work\n\nCo-authored-by: Bo Chen <bo+pairing@corp.com>")
	g := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 36500})
	if _, err := g.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := g.WorkTotals()["bo@corp.com"]; got != 1 {
		t.Errorf("co-author total = %v under bo@corp.com, want 1: %v", got, g.WorkTotals())
	}
	if _, ok := g.WorkTotals()["bo+pairing@corp.com"]; ok {
		t.Error("the plus-tagged trailer address survived as its own person")
	}
}

// TestGitRecentWindowUsesOneClock verifies the recent-window boundary is
// judged against one reading of the clock for the whole walk, pinned the same
// way the scan window is. The commit inside the window is recent, the one
// outside is not, against the injected clock rather than the machine's.
func TestGitRecentWindowUsesOneClock(t *testing.T) {
	t.Parallel()
	dir := newTwoCommitRepo(t, []struct{ Email, Path, Date string }{
		{"alice@corp.com", "billing/impl.go", "2020-05-01T10:00:00Z"},
		{"alice@corp.com", "ledger/impl.go", "2019-06-01T10:00:00Z"},
	})
	g := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 36500})
	// A clock fixed years from the machine's: only code reading this clock,
	// never the wall clock, can place the May commit inside the 180-day window.
	g.now = func() time.Time { return time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC) }
	recs, err := g.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var alice *Record
	for i := range recs {
		if recs[i].Kind == KindPerson && recs[i].Email == "alice@corp.com" {
			alice = &recs[i]
		}
	}
	if alice == nil {
		t.Fatal("no record for alice")
		return
	}
	if !slices.Contains(alice.RecentTopics, "billing") {
		t.Errorf("recent topics = %v, want billing: one month before the pinned clock is recent",
			alice.RecentTopics)
	}
	if slices.Contains(alice.RecentTopics, "ledger") {
		t.Errorf("recent topics = %v, must not hold ledger: a year before the pinned clock is not recent",
			alice.RecentTopics)
	}
}

// writeRepoFile writes one file under the repository root, creating parents.
func writeRepoFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}
