package connector

import (
	"context"
	"strings"
	"testing"
)

// newInternalRepo builds a repository with one commit under internal/, with
// the given origin remote, or none when remote is empty.
func newInternalRepo(t *testing.T, remote, author string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, nil, "init", "-q", "-b", "main")
	if remote != "" {
		runGit(t, dir, nil, "remote", "add", "origin", remote)
	}
	writeRepoFile(t, dir, "internal/impl.go", "content\n")
	runGit(t, dir, nil, "add", "internal/impl.go")
	runGit(t, dir, authorEnv("Dev", author, "2026-01-02T10:00:00Z"), "commit", "-q", "-m", "work")
	return dir
}

// TestMultiRepoPlacesStaySeparate verifies a multi-repository read scopes
// directory keys by repository: every service has an internal/, and pooling
// them reported a system called "internal" that exists nowhere, whose head
// count unioned unrelated teams. A reader checking the systems file against
// their own repositories must find directories that exist.
func TestMultiRepoPlacesStaySeparate(t *testing.T) {
	t.Parallel()
	a := newInternalRepo(t, "https://github.com/acme/svc-a.git", "ana@corp.com")
	b := newInternalRepo(t, "https://github.com/acme/svc-b.git", "bo@corp.com")
	g := NewGitHistory(GitOptions{Paths: []string{a, b}, SinceDays: 36500})
	if _, err := g.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	work := g.DirWork()
	if _, ok := work["internal"]; ok {
		t.Error("a bare internal key survived a two-repository read; the pooled place exists nowhere")
	}
	if holders := work["svc-a/internal"]; len(holders) != 1 || holders["ana@corp.com"] != 1 {
		t.Errorf("svc-a/internal holders = %v, want ana alone", holders)
	}
	if holders := work["svc-b/internal"]; len(holders) != 1 || holders["bo@corp.com"] != 1 {
		t.Errorf("svc-b/internal holders = %v, want bo alone", holders)
	}
	// The label is the repository's short name, never a filesystem path.
	for key := range work {
		if strings.HasPrefix(key, "/") || strings.Contains(key, a) || strings.Contains(key, b) {
			t.Errorf("key %q leaks a filesystem path into the deliverable", key)
		}
	}
}

// TestSingleRepoPlacesKeepBareNames verifies a one-repository read still keys
// directories by their own names: "internal" is the right label when there is
// only one internal.
func TestSingleRepoPlacesKeepBareNames(t *testing.T) {
	t.Parallel()
	dir := newInternalRepo(t, "https://github.com/acme/svc-a.git", "ana@corp.com")
	g := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 36500})
	if _, err := g.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if holders := g.DirWork()["internal"]; len(holders) != 1 || holders["ana@corp.com"] != 1 {
		t.Errorf("internal holders = %v, want the bare key kept for a single repository", holders)
	}
}

// TestMultiRepoLabelsWithoutRemotes verifies clones without remotes still get
// recognizable, distinct labels from their directory names, and the labels
// never carry the directories above them.
func TestMultiRepoLabelsWithoutRemotes(t *testing.T) {
	t.Parallel()
	a := newInternalRepo(t, "", "ana@corp.com")
	b := newInternalRepo(t, "", "bo@corp.com")
	g := NewGitHistory(GitOptions{Paths: []string{a, b}, SinceDays: 36500})
	if _, err := g.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	work := g.DirWork()
	if _, ok := work["internal"]; ok {
		t.Error("a bare internal key survived a two-repository read")
	}
	found := 0
	for key := range work {
		if strings.HasSuffix(key, "/internal") {
			found++
			if strings.Count(key, "/") != 1 {
				t.Errorf("key %q carries more than the repository's own name", key)
			}
		}
	}
	if found != 2 {
		t.Errorf("found %d scoped internal keys, want one per repository; keys=%v", found, workKeys(work))
	}
}

// workKeys lists a work tally's keys for a failure message.
func workKeys(m map[string]map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
