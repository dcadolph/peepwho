package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/fakeapi"
)

// newReviewedRepo builds a repository where a pull request lands through a
// merge naming its number, so the pull-request-to-place link exists, and the
// person who reviewed it never writes a line of the code.
func newReviewedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		cmd.Env = append(cmd.Env, env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	who := func(name, email, when string) []string {
		return []string{
			"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + when,
			"GIT_COMMITTER_NAME=C", "GIT_COMMITTER_EMAIL=c@x.com", "GIT_COMMITTER_DATE=" + when,
		}
	}
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
	run(nil, "init", "-q", "-b", "main")
	write("README.md", "start\n")
	run(nil, "add", "README.md")
	run(who("Base", "base@x.com", "2026-01-01T10:00:00Z"), "commit", "-q", "-m", "init")

	// Enough authored work in scheduler for the place to clear the floor,
	// all of it by one contributor who is not the approver.
	run(nil, "checkout", "-q", "-b", "pr")
	for i := range 40 {
		write(fmt.Sprintf("scheduler/f%02d.go", i), fmt.Sprintf("package scheduler // %d\n", i))
		run(nil, "add", "-A")
		run(who("Contributor", "contrib@x.com",
			fmt.Sprintf("2026-01-02T10:%02d:00Z", i)), "commit", "-q", "-m", "scheduler work")
	}
	run(nil, "checkout", "-q", "main")
	run(who("Merger", "merge@x.com", "2026-01-03T10:00:00Z"),
		"merge", "-q", "--no-ff", "-m", "Merge pull request #42 from fork/pr", "pr")
	return dir
}

// TestAssessPlacesReviewCredit verifies the product, not just the benchmark,
// credits a reviewer with the places their pull request changed. The reviewer
// writes no code at all, so nothing but placed review credit can surface them
// as holding the system.
func TestAssessPlacesReviewCredit(t *testing.T) {
	repo := newReviewedRepo(t)
	gh := (&fakeapi.GitHub{
		Repos: []fakeapi.GitHubRepo{{
			Owner: "acme", Name: "platform",
			Contributors: map[string]int{"contributor": 40},
			Pulls: []fakeapi.GitHubPull{{
				Number: 42, Title: "scheduler work", AuthorLogin: "contributor",
				ReviewedBy: []string{"approver-who-never-commits"},
				UpdatedAt:  "2026-01-03T10:00:00Z", MergedAt: "2026-01-03T10:00:00Z",
			}},
		}},
	}).Server()
	t.Cleanup(gh.Close)

	dir := t.TempDir()
	out := filepath.Join(dir, "assessment")
	t.Setenv("PEEPWHO_GITHUB_TOKEN", "test-token")

	if _, stderr, err := runCmd(t, "assess", "--data-dir", dir,
		"--repo-path", repo, "--github-repo", "acme/platform",
		"--github-url", gh.URL, "--out", out); err != nil {
		t.Fatalf("assess: %v\n%s", err, stderr)
	}

	raw, err := os.ReadFile(filepath.Join(out, "systems.json"))
	if err != nil {
		t.Fatalf("read systems: %v", err)
	}
	var places []struct {
		Dir     string `json:"dir"`
		Holders []struct {
			Name string `json:"name"`
		} `json:"holders"`
	}
	if err := json.Unmarshal(raw, &places); err != nil {
		t.Fatalf("parse systems: %v", err)
	}
	for _, p := range places {
		if p.Dir != "scheduler" {
			continue
		}
		var names []string
		for _, h := range p.Holders {
			names = append(names, h.Name)
		}
		if !slices.ContainsFunc(names, func(n string) bool {
			return strings.Contains(strings.ToLower(n), "approver-who-never-commits")
		}) {
			t.Errorf("scheduler holders = %v; the reviewer who wrote none of it "+
				"was not credited with the place they reviewed", names)
		}
		return
	}
	t.Fatalf("scheduler was not reported as a system; places = %+v", places)
}
