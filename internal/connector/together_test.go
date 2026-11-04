package connector

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// linksFor returns the subjects tied to name in a set of records.
func linksFor(recs []Record, name string) []string {
	for _, r := range recs {
		if r.Kind == KindTopic && r.Name == name {
			var out []string
			for _, l := range r.Links {
				out = append(out, l.To)
			}
			return out
		}
	}
	return nil
}

// touch writes a file under dir and stages it.
func touch(t *testing.T, dir, path string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := fmt.Sprintf("package x // %d\n", len(path))
	if err := os.WriteFile(full, []byte(body+t.Name()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestSubjectsChangedTogetherAreTied checks peepwho learns that two areas belong
// to one body of knowledge from the work itself. Every other notion of
// relatedness it has runs through the people who hold both subjects, which
// cannot then be evidence about those people without arguing in a circle.
func TestSubjectsChangedTogetherAreTied(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "-b", "main")

	// Two areas repeatedly changed in the same commit, by different people, so
	// the tie cannot be coming from a shared author.
	for i := range 6 {
		touch(t, dir, fmt.Sprintf("services/billing/f%d.go", i))
		touch(t, dir, fmt.Sprintf("services/ledger/f%d.go", i))
		gitRun(t, dir, "add", ".")
		who := []string{"ada", "bo", "cy"}[i%3]
		cmd := exec.Command("git", "commit", "--quiet", "-m", "work")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME="+who, "GIT_AUTHOR_EMAIL="+who+"@corp.com",
			"GIT_COMMITTER_NAME="+who, "GIT_COMMITTER_EMAIL="+who+"@corp.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %v: %s", err, out)
		}
	}
	// And one area changed only ever on its own.
	for i := range 6 {
		touch(t, dir, fmt.Sprintf("services/search/f%d.go", i))
		gitRun(t, dir, "add", ".")
		gitRun(t, dir, "commit", "--quiet", "-m", "search work")
	}

	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 3650}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := linksFor(recs, "billing"); !slices.Contains(got, "ledger") {
		t.Errorf("billing ties = %v, want ledger among them", got)
	}
	if got := linksFor(recs, "ledger"); !slices.Contains(got, "billing") {
		t.Errorf("ledger ties = %v, want billing among them", got)
	}
	if got := linksFor(recs, "billing"); slices.Contains(got, "search") {
		t.Errorf("billing ties = %v, want search left out: it never changed alongside", got)
	}
	// The directory every file sits under is touched by every change and is
	// tied to everything by construction, so it must not become a subject's
	// neighbour.
	if got := linksFor(recs, "billing"); slices.Contains(got, "services") {
		t.Errorf("billing ties = %v, want the scaffolding directory left out", got)
	}
}

// TestOnePathIsNotTwoSubjectsMeeting checks the two spellings a single path
// yields are not counted as two subjects being worked on together. The
// tokenizer emits both zwave_js and zwave from one directory, and a name
// agreeing with itself is not evidence of anything.
func TestOnePathIsNotTwoSubjectsMeeting(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "-b", "main")
	for i := range 8 {
		touch(t, dir, fmt.Sprintf("components/zwave_js/f%d.go", i))
		gitRun(t, dir, "add", ".")
		gitRun(t, dir, "commit", "--quiet", "-m", "zwave work")
	}
	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 3650}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := linksFor(recs, "zwave_js"); slices.Contains(got, "zwave") {
		t.Errorf("zwave_js ties = %v, want its own other spelling left out", got)
	}
}

// TestOnePersonConnectionIsCounted checks the walk records how many different
// people have ever worked across a pair of subjects. Both areas can be well
// covered while the link between them has only ever been made once, by one
// person, and nothing that counts experts per subject can see that.
func TestOnePersonConnectionIsCounted(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "-b", "main")

	commit := func(who string, paths ...string) {
		for _, p := range paths {
			touch(t, dir, p)
		}
		gitRun(t, dir, "add", ".")
		cmd := exec.Command("git", "commit", "--quiet", "-m", "work")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME="+who, "GIT_AUTHOR_EMAIL="+who+"@corp.com",
			"GIT_COMMITTER_NAME="+who, "GIT_COMMITTER_EMAIL="+who+"@corp.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %v: %s", err, out)
		}
	}
	// One person, and only that person, ever works across billing and ledger.
	for i := range 5 {
		commit("bridge", fmt.Sprintf("services/billing/b%d.go", i), fmt.Sprintf("services/ledger/l%d.go", i))
	}
	// Several people work across search and index.
	for i, who := range []string{"ada", "bo", "cy", "di", "ez"} {
		commit(who, fmt.Sprintf("services/search/s%d.go", i), fmt.Sprintf("services/indexing/i%d.go", i))
	}

	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 3650}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	find := func(from, to string) TopicLink {
		for _, r := range recs {
			if r.Kind != KindTopic || r.Name != from {
				continue
			}
			for _, l := range r.Links {
				if l.To == to {
					return l
				}
			}
		}
		t.Fatalf("no tie from %q to %q", from, to)
		return TopicLink{}
	}
	if got := find("billing", "ledger"); got.Witnesses != 1 || got.Sole != "bridge@corp.com" {
		t.Errorf("billing to ledger: %d people, sole %q; want one person, bridge", got.Witnesses, got.Sole)
	}
	if got := find("search", "indexing"); got.Witnesses < 2 {
		t.Errorf("search to indexing: %d people, want the several who worked across it", got.Witnesses)
	} else if got.Sole != "" {
		t.Errorf("search to indexing names %q as sole, but several people span it", got.Sole)
	}
}
