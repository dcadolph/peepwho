package connector

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// commitAt writes a file and commits it at a fixed date, so a test can build a
// history with a known shape in time.
func commitAt(t *testing.T, dir, area, who string, when time.Time) {
	t.Helper()
	sub := filepath.Join(dir, area)
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(sub, fmt.Sprintf("f%d.go", when.Unix()))
	if err := os.WriteFile(file, []byte("package x\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	gitRun(t, dir, "add", ".")
	stamp := when.Format(time.RFC3339)
	cmd := exec.Command("git", "commit", "--quiet", "-m", "work on "+area, "--date", stamp)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+who, "GIT_AUTHOR_EMAIL="+who+"@corp.com",
		"GIT_COMMITTER_NAME="+who, "GIT_COMMITTER_EMAIL="+who+"@corp.com",
		"GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
}

// TestUntilDaysStopsTheWindowShort checks history can be read as it stood at a
// past date. Without that, whotoask can only be scored against the same history
// it was given, and cannot be asked the question that matters: would it have
// named the right person before that person's later work made it obvious.
func TestUntilDaysStopsTheWindowShort(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "-b", "main")
	now := time.Now()
	commitAt(t, dir, "billing", "early", now.AddDate(0, 0, -400))
	commitAt(t, dir, "ledger", "late", now.AddDate(0, 0, -10))

	whoIsIn := func(opts GitOptions) []string {
		opts.Paths = []string{dir}
		recs, err := NewGitHistory(opts).Fetch(context.Background())
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		var who []string
		for _, r := range recs {
			who = append(who, r.Name)
		}
		slices.Sort(who)
		return who
	}

	all := whoIsIn(GitOptions{SinceDays: 3650})
	if !slices.Contains(all, "early") || !slices.Contains(all, "late") {
		t.Fatalf("whole window = %v, want both authors", all)
	}
	// Stopping ninety days short must leave the recent work out entirely.
	past := whoIsIn(GitOptions{SinceDays: 3650, UntilDays: 90})
	if !slices.Contains(past, "early") {
		t.Errorf("past window = %v, want the older author kept", past)
	}
	if slices.Contains(past, "late") {
		t.Errorf("past window = %v, want the recent author excluded", past)
	}
}
