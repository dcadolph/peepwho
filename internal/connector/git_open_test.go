package connector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestOpenRepoHandlesEveryLayout checks the faster open path still reaches a
// repository in each shape one comes in. It bypasses go-git's own resolution to
// hold the packfiles open, so every layout that resolution handled has to keep
// working: speed is not worth refusing to read somebody's repository.
func TestOpenRepoHandlesEveryLayout(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// A working copy with a .git directory, which is the ordinary case.
	work := t.TempDir()
	gitRun(t, work, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "billing.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "--quiet", "-m", "add billing")

	// A bare repository, which is what a mirror or a server copy looks like.
	bare := filepath.Join(t.TempDir(), "bare.git")
	gitRun(t, ".", "clone", "--quiet", "--bare", "file://"+work, bare)

	// A linked worktree, whose .git is a file pointing at the real directory.
	tree := filepath.Join(t.TempDir(), "linked")
	gitRun(t, work, "worktree", "add", "--quiet", "--detach", tree)

	for _, test := range []struct {
		Name string
		Path string
	}{
		{"working copy", work},
		{"bare repository", bare},
		{"linked worktree", tree},
	} {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			repo, closeRepo, err := openRepo(test.Path)
			if err != nil {
				t.Fatalf("openRepo(%s): %v", test.Name, err)
			}
			defer func() { _ = closeRepo() }()
			if repo == nil {
				t.Fatalf("openRepo(%s) returned no repository", test.Name)
				return
			}
			if _, err := repo.Head(); err != nil {
				t.Fatalf("head of %s: %v", test.Name, err)
			}

			// And the connector reads it end to end, not just opens it.
			recs, err := NewGitHistory(GitOptions{
				Paths: []string{test.Path}, SinceDays: 3650,
			}).Fetch(context.Background())
			if err != nil {
				t.Fatalf("fetch %s: %v", test.Name, err)
			}
			if len(recs) == 0 {
				t.Errorf("%s produced no records", test.Name)
			}
		})
	}
}
