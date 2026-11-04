package connector

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun runs a git command in dir, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@corp.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@corp.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// TestShallowCloneIsCalledOut checks a truncated history is reported rather
// than passed off as the whole story. A shallow clone reads without error and
// yields an index with almost no memory in it, which is indistinguishable from
// a company where nobody built anything unless peepwho says so.
func TestShallowCloneIsCalledOut(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin := t.TempDir()
	gitRun(t, origin, "init", "--quiet", "-b", "main")
	for _, name := range []string{"billing.go", "ledger.go", "kafka.go", "vault.go", "audit.go"} {
		if err := os.WriteFile(filepath.Join(origin, name), []byte("package x\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		gitRun(t, origin, "add", ".")
		gitRun(t, origin, "commit", "--quiet", "-m", "add "+name)
	}

	shallow := filepath.Join(t.TempDir(), "shallow")
	gitRun(t, ".", "clone", "--quiet", "--depth", "3", "file://"+origin, shallow)

	var log bytes.Buffer
	src := NewGitHistory(GitOptions{Paths: []string{shallow}, SinceDays: 3650, Log: &log})
	recs, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(log.String(), "shallow clone") {
		t.Errorf("log did not mention the truncated history:\n%s", log.String())
	}
	// The history that is present still has to be read. Walking off the end of
	// a shallow clone is where its history stops, not a reason to discard the
	// commits it does have.
	if len(recs) == 0 {
		t.Errorf("a shallow clone produced no records at all:\n%s", log.String())
	}
}

// TestFullCloneIsNotCalledShallow is the other half: an ordinary repository
// must not be accused of having lost its history.
func TestFullCloneIsNotCalledShallow(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "billing.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "--quiet", "-m", "add billing")

	var log bytes.Buffer
	src := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 3650, Log: &log})
	recs, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if strings.Contains(log.String(), "shallow clone") {
		t.Errorf("a full clone was reported as shallow:\n%s", log.String())
	}
	if len(recs) == 0 {
		t.Error("a repository with a commit produced no records")
	}
}
