package ownersbench

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
)

// runGit runs one git command in dir, failing the test on error.
//
// git is given a private HOME and temp area beside the repository, so nothing
// about the machine running the tests reaches it. Without that, these
// fixtures failed intermittently in CI with "unable to create temporary
// file" while never reproducing locally, which is the shape of a test reading
// ambient state rather than its own.
func runGit(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	home := dir + "-githome"
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("git home: %v", err)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HOME="+home, "TMPDIR="+home,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	cmd.Env = append(cmd.Env, env...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s%s", strings.Join(args, " "), err, errb.String(),
			gitFailureState(dir, home))
	}
}

// gitFailureState describes the world at the moment a git command failed, so
// a failure that only ever happens on someone else's machine is diagnosable
// from its output alone. A previous flake in CI said only "unable to create
// temporary file" and cost a long hunt that never reproduced; everything
// gathered here is a thing that hunt wanted to know and could not get.
func gitFailureState(dir, home string) string {
	var b strings.Builder
	b.WriteString("\n--- state when git failed ---\n")
	for _, p := range []string{dir, filepath.Join(dir, ".git"),
		filepath.Join(dir, ".git", "objects"), home, os.TempDir()} {
		fi, err := os.Stat(p)
		switch {
		case err != nil:
			fmt.Fprintf(&b, "  %-40s MISSING (%v)\n", p, err)
		default:
			fmt.Fprintf(&b, "  %-40s mode %v\n", p, fi.Mode())
		}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err == nil {
		fmt.Fprintf(&b, "  free space: %d MB, free inodes: %d\n",
			int64(st.Bavail)*int64(st.Bsize)/(1<<20), st.Ffree)
	}
	fmt.Fprintf(&b, "  TMPDIR=%q HOME=%q\n", os.Getenv("TMPDIR"), os.Getenv("HOME"))
	return b.String()
}

// author fixes one commit's identity and dates.
func author(name, email, date string) []string {
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=C", "GIT_COMMITTER_EMAIL=c@x.com", "GIT_COMMITTER_DATE=" + date,
	}
}

// newBenchRepo builds a repository with two owned areas. In billing the
// OWNERS approver is also the top committer (cohort A). In search the OWNERS
// approver commits a sixth of what the top contributor does and works nowhere
// else, which is the focused-owner shape the lead score is built for, while
// three busier contributors fill git's top three (cohort C). A sweeper
// touches everything, so share ranking alone would name them everywhere.
func newBenchRepo(t *testing.T) string {
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
	commit := func(env []string, msg string, paths ...string) {
		t.Helper()
		runGit(t, dir, nil, append([]string{"add", "--"}, paths...)...)
		runGit(t, dir, env, "commit", "-q", "-m", msg)
	}

	write("OWNERS_ALIASES", "aliases:\n  search-approvers:\n    - quietowner\n")
	write("billing/OWNERS", "approvers:\n  - anna-billing\nreviewers:\n  - somebody\n")
	write("search/OWNERS", "approvers:\n  - search-approvers\n")
	commit(author("Setup", "setup@x.com", "2026-01-02T10:00:00Z"),
		"lay down ownership", "OWNERS_ALIASES", "billing/OWNERS", "search/OWNERS")

	// Minutes advance instead of days, so however many commits the fixture
	// grows to, the dates stay valid and ordered.
	tick := 0
	next := func() string {
		tick++
		return fmt.Sprintf("2026-01-05T10:%02d:%02dZ", tick/60, tick%60)
	}
	// Anna owns billing and does the work there: cohort A by construction.
	anna := func() []string {
		return author("Anna Billing", "1111+anna-billing@users.noreply.github.com", next())
	}
	for i, f := range []string{"invoices.go", "refunds.go", "ledger.go", "retries.go"} {
		for range 3 + i {
			write("billing/"+f, f+next())
			commit(anna(), "billing work", "billing/"+f)
		}
	}
	// Heavy contributes most of search's commits but owns nothing, and like
	// every real heavy contributor is busy across the rest of the tree too,
	// which is what the breadth discount keys on.
	heavy := func() []string { return author("Heavy Contributor", "heavy@x.com", next()) }
	for i, f := range []string{"ranker.go", "scorer.go", "tokenizer.go"} {
		for range 4 + i {
			write("search/"+f, f+next())
			commit(heavy(), "search work", "search/"+f)
		}
	}
	for _, f := range []string{"infra/deploy.go", "infra/metrics.go", "billing/audit.go",
		"platform/queue.go", "platform/config.go"} {
		for range 5 {
			write(f, f+next())
			commit(heavy(), "wide work", f)
		}
	}
	// Two more regulars outrank the quiet owner by touch count, so the top
	// three committers of search never include the owner.
	// A slice, not a map: Go randomizes map iteration, so ranging one here
	// built a different repository on every run and made the cohort split
	// this test asserts depend on the order the runtime happened to pick.
	for _, reg := range []struct{ name, file string }{
		{"Reg One", "cache.go"}, {"Reg Two", "query.go"},
	} {
		n, f := reg.name, reg.file
		who := func() []string {
			// The mail local part must not carry the space in the name.
			return author(n, strings.ReplaceAll(strings.ToLower(n), " ", ".")+"@x.com", next())
		}
		for range 8 {
			write("search/"+f, f+next())
			commit(who(), "search upkeep", "search/"+f)
		}
		for _, other := range []string{"infra/tooling.go", "platform/jobs.go"} {
			for range 6 {
				write(other, n+other+next())
				commit(who(), "elsewhere", other)
			}
		}
	}
	// The quiet owner touches search rarely, and nothing else at all, which
	// is what the lead score keys on.
	quiet := func() []string {
		return author("Quiet Owner", "2222+quietowner@users.noreply.github.com", next())
	}
	for range 6 {
		write("search/design.go", "design"+next())
		commit(quiet(), "hold the search design", "search/design.go")
	}
	// The sweeper touches every area more than anyone.
	sweep := func() []string { return author("Sweeper", "sweep@x.com", next()) }
	for range 6 {
		write("billing/sweep.go", "s"+next())
		write("search/sweep.go", "s"+next())
		write("infra/sweep.go", "s"+next())
		commit(sweep(), "wide cleanup", "billing/sweep.go", "search/sweep.go", "infra/sweep.go")
	}
	return dir
}

// TestLoadTruth covers alias expansion and group dropping.
func TestLoadTruth(t *testing.T) {
	// Deliberately not parallel: this fixture builds a hundred-commit
	// repository with real git, and four of those running at once under the
	// race detector is enough concurrent git to make CI flaky. They finish in
	// seconds serially.
	dir := newBenchRepo(t)
	truth, err := LoadTruth(dir)
	if err != nil {
		t.Fatalf("LoadTruth: %v", err)
	}
	byDir := make(map[string][]string)
	for _, tr := range truth {
		byDir[tr.Dir] = tr.Approvers
	}
	if got := byDir["billing"]; len(got) != 1 || got[0] != "anna-billing" {
		t.Errorf("billing approvers = %v, want [anna-billing]; reviewers must not leak in", got)
	}
	if got := byDir["search"]; len(got) != 1 || got[0] != "quietowner" {
		t.Errorf("search approvers = %v, want the alias expanded to quietowner", got)
	}
}

// TestRunSeparatesCohorts runs the whole bench on the fixture and requires
// the cohort split to behave: billing is cohort A, search is cohort C, and
// the lead-scored index finds the quiet owner where commit counting cannot.
func TestRunSeparatesCohorts(t *testing.T) {
	// Deliberately not parallel: this fixture builds a hundred-commit
	// repository with real git, and four of those running at once under the
	// race detector is enough concurrent git to make CI flaky. They finish in
	// seconds serially.
	dir := newBenchRepo(t)

	git := connector.NewGitHistory(connector.GitOptions{
		Paths: []string{dir}, SinceDays: 36500,
	})
	recs, err := git.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	ix := index.New()
	ix.Build(recs)
	ix.AutoJoin()
	ix.Canonicalize()

	res, err := Run(ix, Config{Repo: dir, SinceDays: 36500, MinCommits: 3,
		DirWork: git.DirWork(), WorkTotals: git.WorkTotals()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	byDir := make(map[string]DirResult)
	for _, d := range res.Dirs {
		byDir[d.Dir] = d
	}
	billing, ok := byDir["billing"]
	if !ok {
		t.Fatalf("billing not judged; result %+v", res)
	}
	if !billing.GitHit || !billing.PeepwhoHit {
		t.Errorf("billing = %+v, want both sides to find the owner who does the work", billing)
	}
	search, ok := byDir["search"]
	if !ok {
		t.Fatalf("search not judged; result %+v", res)
	}
	if search.GitHit {
		t.Errorf("search gitTop = %v unexpectedly contains the quiet owner; "+
			"the fixture no longer separates the cohorts", search.GitTop)
	}
	if !search.PeepwhoHit {
		t.Errorf("search peepwhoTop = %v, want Quiet Owner: the lead score exists "+
			"exactly to find the owner commit counting misses", search.PeepwhoTop)
	}
	if len(res.CohortC()) == 0 {
		t.Error("no cohort C directories; the discriminating cohort is empty")
	}
}
