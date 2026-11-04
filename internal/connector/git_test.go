package connector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// newFixtureRepo creates a git repository with a small history: Alice touches
// terraform twice, Bob touches python once, and a bot commit that must be
// skipped.
func newFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	commit := func(rel, content, name, email string, when time.Time) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("add: %v", err)
		}
		sig := &object.Signature{Name: name, Email: email, When: when}
		if _, err := wt.Commit("touch "+rel, &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	now := time.Now()
	commit("infra/main.tf", "a", "Alice Smith", "alice@corp.com", now.AddDate(0, 0, -30))
	commit("infra/vpc.tf", "b", "Alice Smith", "alice@corp.com", now.AddDate(0, 0, -10))
	commit("app/serve.py", "c", "Bob Jones", "bob@corp.com", now.AddDate(0, 0, -5))
	commit("go.sum", "d", "dependabot[bot]", "12345+dependabot[bot]@users.noreply.github.com",
		now.AddDate(0, 0, -1))
	return dir
}

func TestGitHistoryFetch(t *testing.T) {
	t.Parallel()
	dir := newFixtureRepo(t)
	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	byEmail := make(map[string]Record)
	for _, r := range recs {
		byEmail[r.Email] = r
	}
	if len(byEmail) != 2 {
		t.Fatalf("authors = %d (%v), want 2 with the bot skipped", len(byEmail), recs)
	}

	// Path tokens establish a subject and land in Topics; commit subject words
	// are prose and arrive as weak topics. Both carry affinity.
	all := func(r Record) []string {
		return append(append([]string(nil), r.Topics...), r.WeakTopics...)
	}
	alice := byEmail["alice@corp.com"]
	if !slices.Contains(all(alice), "terraform") || !slices.Contains(all(alice), "infra") {
		t.Errorf("alice topics = %v, weak = %v, want terraform and infra",
			alice.Topics, alice.WeakTopics)
	}
	if alice.Name != "Alice Smith" {
		t.Errorf("alice name = %q", alice.Name)
	}
	wantLatest := time.Now().AddDate(0, 0, -10)
	if alice.Time.Before(wantLatest.Add(-time.Hour)) || alice.Time.After(wantLatest.Add(time.Hour)) {
		t.Errorf("alice time = %v, want near her latest commit %v", alice.Time, wantLatest)
	}

	bob := byEmail["bob@corp.com"]
	if !slices.Contains(all(bob), "python") || !slices.Contains(all(bob), "serve") {
		t.Errorf("bob topics = %v, weak = %v, want python and serve", bob.Topics, bob.WeakTopics)
	}
}

func TestGitHistoryMaxCommits(t *testing.T) {
	t.Parallel()
	dir := newFixtureRepo(t)
	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}, MaxCommits: 1}).
		Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) != 1 || recs[0].Email != "bob@corp.com" {
		t.Errorf("records = %+v, want only the newest human commit (bob)", recs)
	}
}

func TestGitHistoryErrors(t *testing.T) {
	t.Parallel()
	if _, err := NewGitHistory(GitOptions{}).Fetch(context.Background()); !errors.Is(err, ErrNoRepoPaths) {
		t.Errorf("no paths error = %v, want ErrNoRepoPaths", err)
	}
	dir := t.TempDir()
	var log strings.Builder
	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}, Log: &log}).Fetch(context.Background())
	if !errors.Is(err, ErrNoReadableRepo) {
		t.Errorf("Fetch = %v, want ErrNoReadableRepo when every path is skipped", err)
	}
	if len(recs) != 0 {
		t.Errorf("records = %+v, want none from a non-repository directory", recs)
	}
	if !strings.Contains(log.String(), "skipping") {
		t.Errorf("log = %q, want a skip warning", log.String())
	}
}

// TestGitHistoryMailmap verifies a .mailmap merges one person's two commit
// emails into a single record under the canonical identity.
func TestGitHistoryMailmap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	commit := func(rel, name, email string, when time.Time) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("add: %v", err)
		}
		sig := &object.Signature{Name: name, Email: email, When: when}
		if _, err := wt.Commit("c", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	now := time.Now()
	commit("infra/vpc.tf", "Alice P", "alice@personal.com", now.AddDate(0, 0, -20))
	commit("infra/main.tf", "Alice Smith", "alice@corp.com", now.AddDate(0, 0, -5))
	mm := "Alice Smith <alice@corp.com> <alice@personal.com>\n"
	if err := os.WriteFile(filepath.Join(dir, ".mailmap"), []byte(mm), 0o600); err != nil {
		t.Fatalf("write mailmap: %v", err)
	}

	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %d (%+v), want 1 merged author", len(recs), recs)
	}
	if recs[0].Email != "alice@corp.com" || recs[0].Name != "Alice Smith" {
		t.Errorf("record = %+v, want canonical alice@corp.com / Alice Smith", recs[0])
	}
	merged := append(append([]string(nil), recs[0].Topics...), recs[0].WeakTopics...)
	if !slices.Contains(merged, "terraform") {
		t.Errorf("topics = %v, weak = %v, want terraform from both commits",
			recs[0].Topics, recs[0].WeakTopics)
	}
}

// TestGitHistorySkipsBadRepo verifies a bad path is logged and skipped while
// good repositories still contribute records.
func TestGitHistorySkipsBadRepo(t *testing.T) {
	t.Parallel()
	good := newFixtureRepo(t)
	bad := t.TempDir()
	var log strings.Builder
	recs, err := NewGitHistory(GitOptions{Paths: []string{bad, good}, Log: &log}).
		Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch = %v, want the bad path skipped without error", err)
	}
	if len(recs) == 0 {
		t.Error("want records from the good repository")
	}
	if !strings.Contains(log.String(), "skipping") {
		t.Errorf("log = %q, want a skip warning for the bad path", log.String())
	}
}

// TestGitJoinsGitHubByNoreplyLogin verifies a commit made under a GitHub
// noreply email keys the author by their GitHub login, so their commits join
// their pull requests and reviews instead of appearing as a second person.
func TestGitJoinsGitHubByNoreplyLogin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "billing.go"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add("billing.go"); err != nil {
		t.Fatalf("add: %v", err)
	}
	sig := &object.Signature{
		Name: "Octo Dev", Email: "99+octodev@users.noreply.github.com", When: time.Now(),
	}
	if _, err := wt.Commit("billing", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// The record carries the canonical noreply form, the id prefix folded away,
	// which is the same shape util.NormalizeEmail gives the identity graph.
	var octo *Record
	for i := range recs {
		if recs[i].Email == "octodev@users.noreply.github.com" {
			octo = &recs[i]
		}
	}
	if octo == nil {
		t.Fatal("no record for the noreply author")
		return
	}
	if octo.PersonID != "github:octodev" {
		t.Errorf("git author keyed as %q, want github:octodev so it joins GitHub", octo.PersonID)
	}
}

// TestGitStatesWholeDirectoryNamesOnly checks a compound directory name is one
// subject, and the words inside it are searchable without being subjects.
//
// A directory called data_grand_lyon is one integration. Counting its words as
// subjects too reported it as four, put them in the risk table, and connected
// them to each other as if somebody had worked across them. A word that names a
// directory of its own somewhere else keeps its standing: energy is stated by
// homeassistant/components/energy, so it stays a subject wherever else it turns
// up inside a longer name.
func TestGitStatesWholeDirectoryNamesOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	write := func(rel string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	write("components/data_grand_lyon/sensor.py")
	write("components/energy/sensor.py")
	sig := &object.Signature{Name: "Ada", Email: "ada@x.com", When: time.Now()}
	if _, err := wt.Commit("work", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 3650}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var rec Record
	for _, r := range recs {
		if r.Email == "ada@x.com" {
			rec = r
		}
	}
	if rec.Email == "" {
		t.Fatal("the author did not come back at all")
	}
	stated := make(map[string]bool, len(rec.Topics))
	for _, tok := range rec.Topics {
		stated[tok] = true
	}
	all := append(append([]string{}, rec.Topics...), rec.WeakTopics...)

	for _, want := range []string{"data_grand_lyon", "energy"} {
		if !stated[want] {
			t.Errorf("%q is not stated; a whole directory name is the subject", want)
		}
	}
	for _, notSubject := range []string{"grand", "lyon"} {
		if stated[notSubject] {
			t.Errorf("%q is stated, but it is one word of data_grand_lyon and not a subject",
				notSubject)
		}
		if !slices.Contains(all, notSubject) {
			t.Errorf("%q is missing entirely; the words must stay searchable", notSubject)
		}
	}
}

// TestGitResumesFromTheLastCommitRead checks a second run reads only what has
// happened since the first, and reports the new position.
//
// Without this, refreshing an index costs exactly what building it cost: reading
// two years of a large repository took 155 seconds, and a refresh that had
// nothing new to find took 152. Everything below is about that being safe as
// well as fast, because an increment that is folded wrongly does not read slowly,
// it produces a wrong index.
func TestGitResumesFromTheLastCommitRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	add := func(rel string, n int) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(fmt.Sprint(n)), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("add: %v", err)
		}
		sig := &object.Signature{Name: "Ada", Email: "ada@x.com", When: time.Now()}
		if _, err := wt.Commit(fmt.Sprintf("change %d", n), &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	for i := range 3 {
		add("billing/main", i)
	}

	// First read: everything, and a position to resume from.
	first := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 3650})
	if _, err := first.Fetch(context.Background()); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	mark := first.Marks()[dir]
	if mark == "" {
		t.Fatal("no position was reported, so a refresh has nothing to resume from")
	}

	// Nothing has changed, so a resumed read finds nothing and the position holds.
	quiet := NewGitHistory(GitOptions{
		Paths: []string{dir}, SinceDays: 3650, StopAt: map[string]string{dir: mark},
	})
	recs, err := quiet.Fetch(context.Background())
	if err != nil {
		t.Fatalf("quiet Fetch: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("a repository with nothing new produced %d records; a refresh would fold them again", len(recs))
	}
	if got := quiet.Marks()[dir]; got != mark {
		t.Errorf("position moved to %.12s with nothing new; want it to stay at %.12s", got, mark)
	}

	// Two more commits: only those are read.
	add("ledger/main", 4)
	add("ledger/main", 5)
	caught := NewGitHistory(GitOptions{
		Paths: []string{dir}, SinceDays: 3650, StopAt: map[string]string{dir: mark},
	})
	if _, err := caught.Fetch(context.Background()); err != nil {
		t.Fatalf("catch-up Fetch: %v", err)
	}
	if got := caught.Marks()[dir]; got == mark || got == "" {
		t.Errorf("position did not advance after new commits: %.12s", got)
	}
}

// TestGitReadsEverythingWhenThePositionIsGone checks a mark this history no
// longer contains falls back to reading the window, rather than silently
// reading nothing. That is what a rewritten history looks like.
func TestGitReadsEverythingWhenThePositionIsGone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	full := filepath.Join(dir, "billing", "main")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add("billing/main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	sig := &object.Signature{Name: "Ada", Email: "ada@x.com", When: time.Now()}
	if _, err := wt.Commit("only", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var out bytes.Buffer
	src := NewGitHistory(GitOptions{
		Paths: []string{dir}, SinceDays: 3650, Log: &out,
		StopAt: map[string]string{dir: "0000000000000000000000000000000000000000"},
	})
	recs, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) == 0 {
		t.Error("a position this history does not have read nothing; want the whole window")
	}
	if !strings.Contains(out.String(), "reading the whole window") {
		t.Errorf("nothing said the position was gone: %q", out.String())
	}
}

// TestGitKeepsNoPositionForABoundedWindow checks a read that stops short of
// today records no position.
//
// Its newest commit is not the tip, so resuming there would step over
// everything between the two and never read it. Measured: a window ending 30
// days ago followed by a refresh saw 32,564 of 32,886 commits, losing 322 of
// them silently.
func TestGitKeepsNoPositionForABoundedWindow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	full := filepath.Join(dir, "billing", "main")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add("billing/main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	sig := &object.Signature{Name: "Ada", Email: "ada@x.com", When: time.Now().AddDate(0, 0, -90)}
	if _, err := wt.Commit("old", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	src := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 3650, UntilDays: 30})
	if _, err := src.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := src.Marks()[dir]; got != "" {
		t.Errorf("a bounded window recorded %.12s to resume from; want none", got)
	}
}

// TestGitSaysWhenTheCommitCapTruncates covers the failure that is invisible in
// the result. A capped read succeeds, reports a believable number of commits,
// and quietly omits everyone who has not committed recently, so an index built
// over a long history looks the same as a company where almost nobody built
// anything. Measured on a real project the default read 2,000 of 115,755
// commits and found 386 of 5,649 authors, with nothing in the output saying so.
func TestGitSaysWhenTheCommitCapTruncates(t *testing.T) {
	t.Parallel()
	dir := newFixtureRepo(t)

	read := func(max int) string {
		t.Helper()
		var log bytes.Buffer
		g := NewGitHistory(GitOptions{Paths: []string{dir}, MaxCommits: max, Log: &log})
		if _, err := g.Fetch(context.Background()); err != nil {
			t.Fatalf("fetch with cap %d: %v", max, err)
		}
		return log.String()
	}

	const want = "max-commits cap"
	// The fixture holds four commits, so a cap of two has to stop short.
	if out := read(2); !strings.Contains(out, want) {
		t.Errorf("a truncated read said nothing about the cap; log was:\n%s", out)
	}
	// A cap nothing reaches must stay quiet, or the warning is noise that gets
	// ignored on the run where it matters.
	if out := read(500); strings.Contains(out, want) {
		t.Errorf("an untruncated read warned about the cap anyway; log was:\n%s", out)
	}
}

// TestOneWideCommitIsNotAHistoryOfNarrowOnes covers what makes somebody the
// person to ask: having come back to an area, not having touched a lot of it
// once. Counting a subject per file made the two indistinguishable, and on
// home-assistant/core that reported ownership of an integration as having moved
// to somebody with a single twelve-file commit, over the maintainer with
// twenty-seven separate ones.
func TestOneWideCommitIsNotAHistoryOfNarrowOnes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	commit := func(name, email string, when time.Time, rels ...string) {
		t.Helper()
		for _, rel := range rels {
			full := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(full, []byte(when.String()), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := wt.Add(rel); err != nil {
				t.Fatalf("add: %v", err)
			}
		}
		sig := &object.Signature{Name: name, Email: email, When: when}
		if _, err := wt.Commit("touch", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	now := time.Now()
	// One person sweeps eight files of the area in a single change.
	var wide []string
	for i := 0; i < 8; i++ {
		wide = append(wide, filepath.Join("components", "ecovacs", fmt.Sprintf("f%d.py", i)))
	}
	commit("Wide Once", "wide@corp.com", now.AddDate(0, 0, -20), wide...)
	// Another keeps coming back to it, a file at a time.
	for i := 0; i < 4; i++ {
		commit("Steady Hand", "steady@corp.com", now.AddDate(0, 0, -i-1),
			filepath.Join("components", "ecovacs", fmt.Sprintf("g%d.py", i)))
	}

	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	weight := map[string]int{}
	for _, r := range recs {
		if r.Kind != KindPerson {
			continue
		}
		for _, top := range r.Topics {
			if top == "ecovacs" {
				weight[r.Email]++
			}
		}
	}
	if weight["steady@corp.com"] <= weight["wide@corp.com"] {
		t.Errorf("one eight-file change (%d) outweighs four separate ones (%d); "+
			"counting is back to per file",
			weight["wide@corp.com"], weight["steady@corp.com"])
	}
}
