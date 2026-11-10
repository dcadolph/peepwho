package connector

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/go-cmp/cmp"
)

// updateGolden rewrites the captured-git testdata files from the git binary on
// this machine instead of comparing against them.
var updateGolden = flag.Bool("update-golden", false, "rewrite captured git testdata")

// realGitEnv isolates a git invocation from the machine's configuration, so a
// user's mailmap.file, log.mailmap, or diff settings cannot leak into what the
// test captures as git's behavior.
func realGitEnv(home string) []string {
	return append(os.Environ(),
		"HOME="+home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
}

// runGit runs one git command in dir and returns its stdout, failing the test
// on any error. The git binary is required: these tests exist because fixtures
// authored by hand agree with their author, and only git itself can say what
// git does.
func runGit(t *testing.T, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(realGitEnv(t.TempDir()), extraEnv...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return out.String()
}

// authorEnv returns the environment fixing one commit's author, committer, and
// both dates, so the built history is byte-identical between runs.
func authorEnv(name, email, date string) []string {
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=Committer", "GIT_COMMITTER_EMAIL=committer@corp.com",
		"GIT_COMMITTER_DATE=" + date,
	}
}

// realRepoMailmap is the fixture's .mailmap, chosen to hit the semantics git
// actually implements: case-insensitive email and name matching, an email-only
// rule that keeps the commit name, a name-specific rule, and a trailing "#"
// that git does NOT treat as a comment; it parses as a commit name of "#", so
// neither ghost@old.example nor a bare dana@corp.com commit may match it.
const realRepoMailmap = `# Canonical identities.
Robert Jones <bob@corp.com> <BOB@CORP.COM>
Robert Jones <bob@corp.com> Bob The Contractor <bob@contractor.example>
<asa@corp.com> <asa.oberg@old.example>
Dana Roed <dana@corp.com> # <ghost@old.example>
`

// newRealRepo builds a repository with the git CLI: a root commit, authors
// whose spellings only a mailmap unifies, a feature branch and a no-ff merge,
// a rename done with git mv, a bot commit, and a commit with no author email
// made the way fast-import makes them. It returns the repo path and the hash
// of the rename commit.
func newRealRepo(t *testing.T) (dir, renameHash string) {
	t.Helper()
	dir = t.TempDir()
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
	commit := func(msg string, env []string, paths ...string) {
		t.Helper()
		args := append([]string{"add", "--"}, paths...)
		runGit(t, dir, nil, args...)
		runGit(t, dir, env, "commit", "-q", "-m", msg)
	}

	write(".mailmap", realRepoMailmap)
	write("infra/main.tf", "resource \"aws_vpc\" \"main\" {}\n")
	write("infra/vpc.tf", "resource \"aws_subnet\" \"a\" {}\n")
	write("README.md", "networking\n")
	commit("lay down terraform networking",
		authorEnv("Åsa Öberg", "asa@corp.com", "2026-01-05T10:00:00Z"),
		".mailmap", "infra/main.tf", "infra/vpc.tf", "README.md")

	write("app/serve.py", "print('serve')\n")
	commit("serve requests",
		authorEnv("BOB JONES", "BOB@CORP.COM", "2026-01-12T10:00:00Z"), "app/serve.py")

	write("app/serve.py", "print('serve')\nprint('again')\n")
	commit("retry on failure",
		authorEnv("Bob Jones", "bob@corp.com", "2026-01-19T10:00:00Z"), "app/serve.py")

	write("billing/invoices.py", "def invoice(): pass\n")
	commit("bill the invoices",
		authorEnv("Bob The Contractor", "bob@contractor.example", "2026-01-26T10:00:00Z"),
		"billing/invoices.py")

	runGit(t, dir, nil, "checkout", "-q", "-b", "feature")
	write("search/ranker.go", "package search\n")
	commit("rank search results",
		authorEnv("Dana R. Røed", "dana@corp.com", "2026-02-02T10:00:00Z"), "search/ranker.go")
	runGit(t, dir, nil, "checkout", "-q", "main")

	write("docs/runbook.md", "how to run\n")
	commit("write the runbook",
		authorEnv("Åsa Öberg", "asa.oberg@old.example", "2026-02-09T10:00:00Z"), "docs/runbook.md")

	runGit(t, dir, authorEnv("Åsa Öberg", "asa@corp.com", "2026-02-10T10:00:00Z"),
		"merge", "-q", "--no-ff", "-m", "merge feature", "feature")

	runGit(t, dir, nil, "mv", "infra/main.tf", "infra/networking.tf")
	runGit(t, dir, authorEnv("Bob Jones", "bob@corp.com", "2026-02-16T10:00:00Z"),
		"commit", "-q", "-m", "rename main to networking")
	renameHash = strings.TrimSpace(runGit(t, dir, nil, "rev-parse", "HEAD"))

	write("go.sum", "module deps\n")
	commit("bump deps",
		authorEnv("dependabot[bot]", "49699333+dependabot[bot]@users.noreply.github.com",
			"2026-02-20T10:00:00Z"), "go.sum")

	// A commit with no author email, the shape fast-import produces. git commit
	// refuses to make one, so build it from the current tree directly.
	tree := strings.TrimSpace(runGit(t, dir, nil, "write-tree"))
	hash := strings.TrimSpace(runGit(t, dir,
		authorEnv("No Email", "", "2026-02-21T10:00:00Z"),
		"commit-tree", tree, "-p", "HEAD", "-m", "who wrote this"))
	runGit(t, dir, nil, "update-ref", "refs/heads/main", hash)
	runGit(t, dir, nil, "reset", "-q", "--hard", "main")
	return dir, renameHash
}

// hashOf parses a hex commit hash, failing the test on garbage.
func hashOf(t *testing.T, hex string) plumbing.Hash {
	t.Helper()
	h := plumbing.NewHash(hex)
	if h.IsZero() {
		t.Fatalf("bad hash %q", hex)
	}
	return h
}

// checkGolden compares got against the named testdata file, rewriting the file
// under -update-golden.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update-golden to create): %v", path, err)
	}
	if diff := cmp.Diff(string(want), got); diff != "" {
		t.Errorf("%s drifted from captured git behavior (-want +got):\n%s", name, diff)
	}
}

// TestRealMailmapMatchesGit holds whotoask's mailmap resolution to git
// check-mailmap over the identities a real history contains: case-varied
// emails and names, an email-only rule, a name-specific rule, a would-be
// trailing comment, and identities no rule touches.
func TestRealMailmapMatchesGit(t *testing.T) {
	t.Parallel()
	dir, _ := newRealRepo(t)
	mm := loadMailmap(dir)
	if mm == nil {
		t.Fatal("loadMailmap returned nil for a repo with a .mailmap")
		return
	}

	idents := []struct{ Name, Email string }{
		{"BOB JONES", "BOB@CORP.COM"},
		{"bob jones", "bob@CORP.com"},
		{"Bob Jones", "bob@corp.com"},
		{"Bob The Contractor", "bob@contractor.example"},
		{"BOB THE CONTRACTOR", "bob@contractor.example"},
		{"Bob The Contractor", "other@example.com"},
		{"Åsa Öberg", "ASA.OBERG@OLD.EXAMPLE"},
		{"Ghost", "ghost@old.example"},
		{"Dana Whatever", "dana@corp.com"},
		{"Unmapped Person", "nobody@example.com"},
	}

	var report strings.Builder
	for _, id := range idents {
		in := fmt.Sprintf("%s <%s>", id.Name, id.Email)
		fromGit := strings.TrimSpace(runGit(t, dir, nil, "check-mailmap", in))
		name, email := mm.resolve(id.Name, id.Email)
		got := fmt.Sprintf("%s <%s>", name, email)
		if got != fromGit {
			t.Errorf("resolve(%q) = %q, git check-mailmap says %q", in, got, fromGit)
		}
		fmt.Fprintf(&report, "%s => %s\n", in, fromGit)
	}
	checkGolden(t, "git_real_mailmap.golden", report.String())
}

// TestRealAuthorsMatchShortlog holds the connector's author set to git
// shortlog over the same history: everyone shortlog counts must appear, less
// the bot and the email-less commit, and both of those must be reported to
// the operator by name rather than dropped in silence.
func TestRealAuthorsMatchShortlog(t *testing.T) {
	t.Parallel()
	dir, _ := newRealRepo(t)

	shortlog := runGit(t, dir, nil, "shortlog", "-sne", "--no-merges", "HEAD")
	checkGolden(t, "git_real_shortlog.golden", shortlog)

	// Parse "count\tName <email>" lines into the author set git sees.
	wantAuthors := make(map[string]string)
	for line := range strings.SplitSeq(strings.TrimSpace(shortlog), "\n") {
		_, ident, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("unexpected shortlog line %q", line)
		}
		lt := strings.LastIndex(ident, "<")
		name := strings.TrimSpace(ident[:lt])
		email := strings.ToLower(strings.Trim(ident[lt:], "<>"))
		wantAuthors[email] = name
	}
	// The two named drops: automation, and a commit nobody can be credited for.
	delete(wantAuthors, "49699333+dependabot[bot]@users.noreply.github.com")
	delete(wantAuthors, "")

	var log strings.Builder
	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 36500, Log: &log}).
		Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	gotAuthors := make(map[string]string)
	for _, r := range recs {
		if r.Kind == KindPerson {
			gotAuthors[r.Email] = r.Name
		}
	}
	if diff := cmp.Diff(wantAuthors, gotAuthors); diff != "" {
		t.Errorf("authors disagree with git shortlog (-shortlog +whotoask):\n%s", diff)
	}

	for _, want := range []string{
		"skipped 1 commits from automation accounts: " +
			"dependabot[bot] <49699333+dependabot[bot]@users.noreply.github.com>",
		"1 commits have no author email",
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q; full log:\n%s", want, log.String())
		}
	}
}

// TestRealRenameCommitPaths holds changedPaths to git's own listing of a git
// mv commit, with rename detection off the way whotoask reads it: the old and
// the new path both count as touched.
func TestRealRenameCommitPaths(t *testing.T) {
	t.Parallel()
	dir, renameHash := newRealRepo(t)

	out := runGit(t, dir, nil, "show", "--name-only", "--no-renames", "--format=", renameHash)
	want := strings.Fields(strings.TrimSpace(out))
	slices.Sort(want)

	repo, closeRepo, err := openRepo(dir)
	if err != nil {
		t.Fatalf("openRepo: %v", err)
	}
	defer func() { _ = closeRepo() }()
	commit, err := repo.CommitObject(hashOf(t, renameHash))
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}
	got, err := changedPaths(commit)
	if err != nil {
		t.Fatalf("changedPaths: %v", err)
	}
	slices.Sort(got)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("rename commit paths (-git +whotoask):\n%s", diff)
	}
}

// TestRealRepoTopics spot-checks that the history built with real git mines
// into the topics people would ask about: the renamed path's new name reaches
// Bob, and Dana's branch work survives the merge as her own.
func TestRealRepoTopics(t *testing.T) {
	t.Parallel()
	dir, _ := newRealRepo(t)
	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 36500}).
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

	all := func(r Record) []string {
		return append(append([]string(nil), r.Topics...), r.WeakTopics...)
	}
	bob := byEmail["bob@corp.com"]
	if !slices.Contains(all(bob), "networking") {
		t.Errorf("bob topics %v lack networking from the renamed path", all(bob))
	}
	if bob.Name != "Robert Jones" {
		t.Errorf("bob name = %q, want the mailmap's Robert Jones", bob.Name)
	}
	dana := byEmail["dana@corp.com"]
	if !slices.Contains(all(dana), "search") {
		t.Errorf("dana topics %v lack search from her branch commit", all(dana))
	}
	// The Dana line's trailing "#" is not a comment to git: it turns the rule
	// into one matching a commit name of "#", so Dana keeps her commit name.
	if dana.Name != "Dana R. Røed" {
		t.Errorf("dana name = %q, want her commit name kept", dana.Name)
	}
	asa := byEmail["asa@corp.com"]
	if !slices.Contains(all(asa), "terraform") {
		t.Errorf("asa topics %v lack terraform", all(asa))
	}
}

// TestGitFetchDeterministic ingests one real repository twice with the walk
// spread across many workers, and requires identical records. Worker count
// and goroutine scheduling must never reach the output: the tallies merge
// into maps and the maps must leave in a fixed order.
func TestGitFetchDeterministic(t *testing.T) {
	t.Parallel()
	dir, _ := newRealRepo(t)
	fetch := func() []Record {
		t.Helper()
		recs, err := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 36500, Workers: 8}).
			Fetch(context.Background())
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		return recs
	}
	first := fetch()
	for run := 2; run <= 4; run++ {
		if diff := cmp.Diff(first, fetch()); diff != "" {
			t.Fatalf("run %d differs from run 1:\n%s", run, diff)
		}
	}
}
