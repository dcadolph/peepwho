package connector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/peepwho/internal/github"
)

// newPullRepo builds a repository whose changes land through merge commits
// naming their pull request, the shape GitHub writes, alongside plenty of
// ordinary authored commits. Its origin remote names acme/platform, the
// identity pull request keys carry.
func newPullRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, nil, "init", "-q", "-b", "main")
	runGit(t, dir, nil, "remote", "add", "origin", "https://github.com/acme/platform.git")
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
	write("README.md", "start\n")
	runGit(t, dir, nil, "add", "README.md")
	runGit(t, dir, authorEnv("Base", "base@x.com", "2026-01-01T10:00:00Z"), "commit", "-q", "-m", "init")

	// Two pull requests, each landing on its own branch through a no-ff merge.
	for i, area := range []string{"scheduler", "storage"} {
		branch := fmt.Sprintf("pr%d", i+1)
		runGit(t, dir, nil, "checkout", "-q", "-b", branch)
		write(area+"/impl.go", "package "+area+"\n")
		runGit(t, dir, nil, "add", area+"/impl.go")
		runGit(t, dir, authorEnv("Contributor", "contrib@x.com",
			fmt.Sprintf("2026-01-%02dT10:00:00Z", i+2)), "commit", "-q", "-m", "work on "+area)
		runGit(t, dir, nil, "checkout", "-q", "main")
		runGit(t, dir, authorEnv("Merger", "merge@x.com",
			fmt.Sprintf("2026-01-%02dT11:00:00Z", i+2)),
			"merge", "-q", "--no-ff", "-m",
			fmt.Sprintf("Merge pull request #%d from fork/%s", 100+i, branch), branch)
	}
	return dir
}

// TestPullDirsFromMerges verifies the local pull-request-to-places link: a
// merge names its pull request, and the directories it landed are recorded
// under a repository-scoped key, so a reviewer can be credited with the
// places rather than the prose and never with another repository's places.
func TestPullDirsFromMerges(t *testing.T) {
	t.Parallel()
	dir := newPullRepo(t)
	var log strings.Builder
	g := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 36500, Log: &log})
	if _, err := g.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	pulls := g.PullDirs()
	if len(pulls) != 2 {
		t.Fatalf("pull dirs = %v, want both merges recorded", pulls)
	}
	if got := pulls[PullRef{Repo: "acme/platform", Number: 100}]; !slices.Contains(got, "scheduler") {
		t.Errorf("pull acme/platform#100 dirs = %v, want scheduler", got)
	}
	if got := pulls[PullRef{Repo: "acme/platform", Number: 101}]; !slices.Contains(got, "storage") {
		t.Errorf("pull acme/platform#101 dirs = %v, want storage", got)
	}
	if !strings.Contains(log.String(), "keyed to acme/platform") {
		t.Errorf("log does not say where the pull request link points; log:\n%s", log.String())
	}
	if diff := cmp.Diff([]string{"acme/platform"}, g.RepoIDs()); diff != "" {
		t.Errorf("repo ids mismatch (-want +got):\n%s", diff)
	}
	// A merge is not authorship: the merger must not be credited for it.
	recs, err := g.Fetch(context.Background())
	if err != nil {
		t.Fatalf("refetch: %v", err)
	}
	for _, r := range recs {
		if r.Kind == KindPerson && r.Email == "merge@x.com" {
			t.Errorf("the merger was credited as an author: %+v", r)
		}
	}
}

// TestPullDirsCollideAcrossReposNoLonger pins the collision fix: two
// repositories each landing a pull request numbered 100 must record two
// separate entries, where the bare number as key silently let one overwrite
// the other and which one won depended on order. With two repositories read,
// the recorded directories carry each repository's label, matching the keys
// the place tally now uses.
func TestPullDirsCollideAcrossReposNoLonger(t *testing.T) {
	t.Parallel()
	a, b := newPullRepo(t), newPullRepo(t)
	runGit(t, b, nil, "remote", "set-url", "origin", "git@github.com:acme/website.git")
	g := NewGitHistory(GitOptions{Paths: []string{a, b}, SinceDays: 36500})
	if _, err := g.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	pulls := g.PullDirs()
	if len(pulls) != 4 {
		t.Fatalf("pull dirs = %d entries, want 4: two repos with two pull requests each", len(pulls))
	}
	forA := pulls[PullRef{Repo: "acme/platform", Number: 100}]
	forB := pulls[PullRef{Repo: "acme/website", Number: 100}]
	if !slices.Contains(forA, "platform/scheduler") || !slices.Contains(forB, "website/scheduler") {
		t.Errorf("pull #100 dirs: platform=%v website=%v, want each repo's own scoped scheduler", forA, forB)
	}
	if diff := cmp.Diff([]string{"acme/platform", "acme/website"}, g.RepoIDs()); diff != "" {
		t.Errorf("repo ids mismatch (-want +got):\n%s", diff)
	}
}

// TestPullDirsWithoutARemoteKeepTheirNumbers verifies an unremoted clone, the
// shape of a data-room copy, still records its pull links: keyed with an
// empty repository so the resolver can join them where the number is
// unambiguous, never dropped, and the missing identity is reported.
func TestPullDirsWithoutARemoteKeepTheirNumbers(t *testing.T) {
	t.Parallel()
	dir := newPullRepo(t)
	runGit(t, dir, nil, "remote", "remove", "origin")
	var log strings.Builder
	g := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 36500, Log: &log})
	if _, err := g.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	pulls := g.PullDirs()
	if got := pulls[PullRef{Number: 100}]; !slices.Contains(got, "scheduler") {
		t.Errorf("pull #100 dirs = %v, want scheduler under an empty-repo key", got)
	}
	if got := pulls[PullRef{Number: 101}]; !slices.Contains(got, "storage") {
		t.Errorf("pull #101 dirs = %v, want storage under an empty-repo key", got)
	}
	if !strings.Contains(log.String(), "no remote names the owner/name") {
		t.Errorf("log does not report the missing repository identity; log:\n%s", log.String())
	}
	if got := g.RepoIDs(); len(got) != 0 {
		t.Errorf("repo ids = %v, want none from a clone without remotes", got)
	}
}

// TestAmbiguousUnremotedNumbersRetire verifies that when two clones with no
// repository identity claim the same pull number, the empty-repo key is
// retired rather than kept for whichever clone merged last: joining it would
// hand one clone's reviewers the other's directories on path order.
func TestAmbiguousUnremotedNumbersRetire(t *testing.T) {
	t.Parallel()
	a, b := newPullRepo(t), newPullRepo(t)
	runGit(t, a, nil, "remote", "remove", "origin")
	runGit(t, b, nil, "remote", "remove", "origin")
	g := NewGitHistory(GitOptions{Paths: []string{a, b}, SinceDays: 36500})
	if _, err := g.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	pulls := g.PullDirs()
	for _, n := range []int{100, 101} {
		if got, ok := pulls[PullRef{Number: n}]; ok {
			t.Errorf("pull #%d = %v; a number two unidentified clones share must be retired", n, got)
		}
	}
}

// TestPullRefsMatchAcrossConnectors proves the two producers of the join key
// agree end to end: the git history keyed by the clone's origin remote, and
// the GitHub connector keyed by its --github-repo value, land review data and
// changed places under the same keys for the same pull requests.
func TestPullRefsMatchAcrossConnectors(t *testing.T) {
	t.Parallel()
	dir := newPullRepo(t)
	g := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 36500})
	if _, err := g.Fetch(context.Background()); err != nil {
		t.Fatalf("git fetch: %v", err)
	}

	// The flag value keeps the case the user typed, so the mock answers the
	// mixed-case paths the client will actually request.
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/Acme/Platform", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"platform","full_name":"acme/platform"}`)
	})
	mux.HandleFunc("/repos/Acme/Platform/contributors", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	mux.HandleFunc("/repos/Acme/Platform/pulls", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"number":100,"title":"scheduler fix","user":{"login":"jane"},`+
			`"requested_reviewers":[{"login":"bob"}],"updated_at":"2026-01-02T11:00:00Z"},`+
			`{"number":101,"title":"storage fix","user":{"login":"jane"},`+
			`"requested_reviewers":[{"login":"kim"}],"updated_at":"2026-01-03T11:00:00Z"}]`)
	})
	mux.HandleFunc("/repos/Acme/Platform/issues", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// The flag value differs in case from the remote; the key must not care.
	gh := NewGitHubWithClient(github.New("token", github.WithBaseURL(srv.URL)),
		GitHubOptions{Repos: []string{"Acme/Platform"}})
	if _, err := gh.Fetch(context.Background()); err != nil {
		t.Fatalf("github fetch: %v", err)
	}

	dirs, people := g.PullDirs(), gh.PullPeople()
	if len(people) != 2 {
		t.Fatalf("pull people = %v, want both pull requests", people)
	}
	for key, dir := range dirs {
		if _, ok := people[key]; !ok {
			t.Errorf("git key %+v (dirs %v) has no matching GitHub entry; the join is broken", key, dir)
		}
	}
	if got := people[PullRef{Repo: "acme/platform", Number: 100}]; !slices.Contains(got, "bob") {
		t.Errorf("pull acme/platform#100 people = %v, want reviewer bob", got)
	}
}

// TestMergesDoNotConsumeTheCommitCap verifies the cap counts authorship. A
// repository that lands everything through merges once starved itself of the
// commits the cap exists to read.
func TestMergesDoNotConsumeTheCommitCap(t *testing.T) {
	t.Parallel()
	dir := newPullRepo(t)
	var log strings.Builder
	recs, err := NewGitHistory(GitOptions{
		Paths: []string{dir}, SinceDays: 36500, MaxCommits: 3, Log: &log,
	}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	found := false
	for _, r := range recs {
		if r.Kind == KindPerson && r.Email == "contrib@x.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("the contributor is missing under a cap of 3; merges ate it. records=%+v", recs)
	}
}
