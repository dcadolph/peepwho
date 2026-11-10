package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testGitRepo is a scratch repository that tests commit into with fixed
// identities and dates.
type testGitRepo struct {
	// t is the test the repository belongs to.
	t *testing.T
	// Dir is the repository root.
	Dir string
}

// newTestGitRepo initializes an empty repository on main.
func newTestGitRepo(t *testing.T) *testGitRepo {
	t.Helper()
	r := &testGitRepo{t: t, Dir: t.TempDir()}
	r.git(nil, "init", "-q", "-b", "main")
	return r
}

// git runs one git command in the repository with extra environment.
func (r *testGitRepo) git(env []string, args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	cmd.Env = append(cmd.Env, env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// commit writes rel with content and commits it as name and email at when.
func (r *testGitRepo) commit(name, email string, when time.Time, rel, content string) {
	r.t.Helper()
	full := filepath.Join(r.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		r.t.Fatalf("write: %v", err)
	}
	stamp := when.UTC().Format(time.RFC3339)
	r.git(nil, "add", "--", rel)
	r.git([]string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + stamp,
		"GIT_COMMITTER_NAME=C", "GIT_COMMITTER_EMAIL=c@x.com", "GIT_COMMITTER_DATE=" + stamp,
	}, "commit", "-q", "-m", "work on "+rel)
}

// newOwnersRepo builds a repository whose billing OWNERS approver does the
// billing work, so the ownership benchmark judges one directory as a hit.
func newOwnersRepo(t *testing.T) string {
	t.Helper()
	r := newTestGitRepo(t)
	base := time.Now().AddDate(0, 0, -30)
	r.commit("Setup", "setup@x.com", base, "billing/OWNERS", "approvers:\n  - anna-billing\n")
	for i := range 6 {
		r.commit("Anna Billing", "1111+anna-billing@users.noreply.github.com",
			base.Add(time.Duration(i+1)*time.Hour), fmt.Sprintf("billing/f%d.go", i), "package billing\n")
	}
	return r.Dir
}

// TestEvalOwnersScores runs the ownership benchmark on a repository where
// the declared approver does the work, in both output forms.
func TestEvalOwnersScores(t *testing.T) {
	t.Parallel()
	repo := newOwnersRepo(t)
	args := []string{"eval", "owners", "--repo", repo, "--since-days", "365", "--min-commits", "3"}

	out, stderr, err := runCmd(t, args...)
	if err != nil {
		t.Fatalf("eval owners: %v\n%s", err, stderr)
	}
	for _, want := range []string{"judged 1 directories", "whotoask names an approver in its top 3:  1/1",
		"Y", "billing"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("eval owners output lacks %q:\n%s", want, out)
		}
	}

	out, _, err = runCmd(t, append(args, "--json")...)
	if err != nil {
		t.Fatalf("eval owners --json: %v", err)
	}
	var res struct {
		// TruthDirs is how many directories had OWNERS truth.
		TruthDirs int
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if res.TruthDirs != 1 {
		t.Errorf("TruthDirs = %d, want 1", res.TruthDirs)
	}
}

// TestEvalOwnersWithProbe verifies a saved probe replaces the git tally and
// that answer topics are credited from its episodes.
func TestEvalOwnersWithProbe(t *testing.T) {
	t.Parallel()
	repo := newOwnersRepo(t)
	probe := filepath.Join(t.TempDir(), "probe.json")
	empty := []byte(`{"pullDirs":{},"pullPeople":{},"episodes":[]}`)
	if err := os.WriteFile(probe, empty, 0o600); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	_, stderr, err := runCmd(t, "eval", "owners", "--repo", repo, "--since-days", "365",
		"--min-commits", "3", "--probe", probe, "--credit", "answered", "--answer-topics")
	if err != nil {
		t.Fatalf("eval owners --probe: %v\n%s", err, stderr)
	}
	for _, want := range []string{"probe: 0 pull requests, 0 episodes, credit=answered",
		"answer-hood: 0 episodes credited"} {
		if !strings.Contains(string(stderr), want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}

	_, _, err = runCmd(t, "eval", "owners", "--repo", repo, "--probe",
		filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Error("eval owners with a missing probe: want error, got nil")
	}
}

// TestEvalOwnersUseIndex verifies --use-index scores the saved index and
// reports a missing one as no index.
func TestEvalOwnersUseIndex(t *testing.T) {
	t.Parallel()
	repo := newOwnersRepo(t)
	dir := t.TempDir()
	_, _, err := runCmd(t, "eval", "owners", "--repo", repo, "--use-index", "--data-dir", dir)
	if !errors.Is(err, ErrNoIndex) {
		t.Fatalf("use-index with no index: err = %v, want ErrNoIndex", err)
	}
	if _, stderr, err := runCmd(t, "index", "--data-dir", dir, "--source", "git", "--repo-path", repo,
		"--git-since-days", "365"); err != nil {
		t.Fatalf("index: %v\n%s", err, stderr)
	}
	out, stderr, err := runCmd(t, "eval", "owners", "--repo", repo, "--since-days", "365",
		"--min-commits", "3", "--use-index", "--data-dir", dir)
	if err != nil {
		t.Fatalf("eval owners --use-index: %v\n%s", err, stderr)
	}
	if !strings.Contains(string(out), "judged 1 directories") {
		t.Errorf("use-index output lacks the judged count:\n%s", out)
	}
}

// TestEvalHoldout runs the holdout on a repository whose area changes hands
// across the cutoff, and checks the argument errors.
func TestEvalHoldout(t *testing.T) {
	t.Parallel()
	r := newTestGitRepo(t)
	now := time.Now()
	for i := range 4 {
		r.commit("Early Person", "early@x.com", now.AddDate(0, 0, -400+i),
			fmt.Sprintf("area/early%d.go", i), "early\n")
	}
	for i := range 4 {
		r.commit("Late Person", "late@x.com", now.AddDate(0, 0, -60+i),
			fmt.Sprintf("area/late%d.go", i), "late\n")
	}
	args := []string{"eval", "holdout", "--repo", r.Dir, "--since-days", "730", "--cutoff-days", "180",
		"--min-past", "2", "--min-future", "2"}

	out, stderr, err := runCmd(t, args...)
	if err != nil {
		t.Fatalf("eval holdout: %v\n%s", err, stderr)
	}
	if !strings.Contains(string(out), "does concentration predict fragility") {
		t.Errorf("holdout output lacks the survival section:\n%s", out)
	}
	out, _, err = runCmd(t, append(args, "--json")...)
	if err != nil {
		t.Fatalf("eval holdout --json: %v", err)
	}
	if !json.Valid(out) {
		t.Errorf("eval holdout --json is not JSON:\n%s", out)
	}

	tests := []struct {
		Args []string
		Want error
	}{
		{Args: []string{"eval", "holdout"}, Want: ErrBadArgs}, // Test 0: No repo.
		{ // Test 1: The cutoff must sit inside the window.
			Args: []string{"eval", "holdout", "--repo", r.Dir,
				"--since-days", "100", "--cutoff-days", "200"},
			Want: ErrBadArgs,
		},
	}
	for testNum, test := range tests {
		if _, _, err := runCmd(t, test.Args...); !errors.Is(err, test.Want) {
			t.Errorf("test %d: err = %v, want %v", testNum, err, test.Want)
		}
	}
}

// TestEvalMeasureSaveAndBaseline measures the saved demo, saves the result,
// and compares a second run against it.
func TestEvalMeasureSaveAndBaseline(t *testing.T) {
	t.Parallel()
	dir := savedDemo(t)
	saved := filepath.Join(t.TempDir(), "before.json")

	out, _, err := runCmd(t, "eval", "--data-dir", dir, "--save", saved, "--human")
	if err != nil {
		t.Fatalf("eval --save: %v", err)
	}
	if !strings.Contains(string(out), "Saved to "+saved) {
		t.Errorf("eval --save does not say where it saved:\n%s", out)
	}
	out, _, err = runCmd(t, "eval", "--data-dir", dir, "--baseline", saved, "--json")
	if err != nil {
		t.Fatalf("eval --baseline: %v", err)
	}
	var view struct {
		// Comparable reports whether the two runs cover the same sources.
		Comparable bool `json:"comparable"`
		// SameQuestions reports whether both runs judged the same areas.
		SameQuestions bool `json:"sameQuestions"`
	}
	if err := json.Unmarshal(out, &view); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if !view.Comparable || !view.SameQuestions {
		t.Errorf("a run compared with itself: comparable=%t sameQuestions=%t, want both true",
			view.Comparable, view.SameQuestions)
	}
	if _, _, err := runCmd(t, "eval", "--data-dir", dir, "--baseline",
		filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("eval with a missing baseline: want error, got nil")
	}
	if _, _, err := runCmd(t, "eval", "--data-dir", t.TempDir()); !errors.Is(err, ErrNoIndex) {
		t.Errorf("eval with no index: err = %v, want ErrNoIndex", err)
	}
}
