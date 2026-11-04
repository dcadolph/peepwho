package ownersbench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
)

// newHoldoutRepo builds a repository with a clear before and after: one
// person works an area early and stops, another arrives later, and a bot
// commits throughout. Dates are relative to now so the day-based cutoff
// lands between the two eras.
func newHoldoutRepo(t *testing.T) string {
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
	commit := func(who []string, msg string) {
		t.Helper()
		runGit(t, dir, nil, "add", "-A")
		runGit(t, dir, who, "commit", "-q", "-m", msg)
	}
	// Two years ago: the early era, well before a one-year cutoff.
	for i := range 12 {
		write(fmt.Sprintf("area/early%02d.go", i), fmt.Sprintf("early %d\n", i))
		commit(relAuthor("Early Person", "early@x.com", 730-i), "early work")
	}
	// A bot commits across both eras and must never be anybody's answer.
	// Commits are made in date order, because a windowed git walk prunes on
	// dates and an out-of-order history hides commits from it.
	for i := range 20 {
		write(fmt.Sprintf("area/dep%02d.txt", i), "bump\n")
		commit(relAuthor("dependabot[bot]", "dependabot[bot]@users.noreply.github.com",
			700-i*30), "bump deps")
	}
	// Two months ago: the late era, inside the future window.
	for i := range 12 {
		write(fmt.Sprintf("area/late%02d.go", i), fmt.Sprintf("late %d\n", i))
		commit(relAuthor("Late Person", "late@x.com", 60-i), "late work")
	}
	return dir
}

// relAuthor fixes a commit's identity at a number of days before now. Git's
// commit date parser wants a real timestamp rather than an approximate
// phrase, so the date is computed here.
func relAuthor(name, email string, daysAgo int) []string {
	when := time.Now().AddDate(0, 0, -daysAgo).Format(time.RFC3339)
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + when,
		"GIT_COMMITTER_NAME=C", "GIT_COMMITTER_EMAIL=c@x.com", "GIT_COMMITTER_DATE=" + when,
	}
}

// TestRunHoldoutHidesTheFuture verifies the holdout's two load-bearing
// properties: the index sees nothing after the cutoff, so a prediction cannot
// be made from the answer, and automation never appears as truth.
func TestRunHoldoutHidesTheFuture(t *testing.T) {
	// Deliberately not parallel: this fixture builds a hundred-commit
	// repository with real git, and four of those running at once under the
	// race detector is enough concurrent git to make CI flaky. They finish in
	// seconds serially.
	dir := newHoldoutRepo(t)

	git := connector.NewGitHistory(connector.GitOptions{
		Paths: []string{dir}, SinceDays: 1095, UntilDays: 365,
	})
	recs, err := git.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	ix := index.New()
	ix.Build(recs)
	ix.AutoJoin()
	ix.Canonicalize()

	// The late person worked only inside the future window, so nothing the
	// index holds may mention them.
	for _, p := range ix.Graph.People {
		if p.Email == "late@x.com" {
			t.Fatal("the future leaked into the index; a prediction could read the answer")
		}
	}

	res, err := RunHoldout(ix, HoldoutConfig{
		Repo: dir, SinceDays: 1095, CutoffDays: 365, MinPast: 5, MinFuture: 5,
	})
	if err != nil {
		t.Fatalf("RunHoldout: %v", err)
	}
	var area *HoldoutDir
	for i := range res.Dirs {
		if res.Dirs[i].Dir == "area" {
			area = &res.Dirs[i]
		}
	}
	if area == nil {
		t.Fatalf("area was not judged; dirs = %+v", res.Dirs)
		return
	}
	for _, n := range area.Actual {
		if connector.IsAutomationName(n) {
			t.Errorf("truth = %v; automation must never be the answer", area.Actual)
		}
	}
	if len(area.Actual) == 0 {
		t.Error("no human truth for a directory with human work after the cutoff")
	}
	// Both predictors saw only the early era, so neither can have named the
	// late person: the holdout is measuring prediction, not recall.
	for _, n := range append(area.Peepwho, area.Baseline...) {
		if n == "Late Person" {
			t.Errorf("a predictor named somebody who only appears after the cutoff: %+v", area)
		}
	}
}
