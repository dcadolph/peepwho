package ownersbench

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/github"
	"github.com/dcadolph/peepwho/internal/index"
)

// TestReviewSignalProbe is a one-off experiment runner, gated behind env
// vars: it merges a git-history index with a year of live GitHub review and
// pull data for one repository and saves both, so the owners benchmark can
// measure what review signal is worth. It is not a test of anything.
//
// It saves the read rather than a scored tally. Which participants earn credit
// is a scoring question, and answering it here would mean one forge read per
// arm, on a forge whose pull requests move between reads.
func TestReviewSignalProbe(t *testing.T) {
	repoPath := os.Getenv("PEEPWHO_PROBE_REPO")
	repoFull := os.Getenv("PEEPWHO_PROBE_GITHUB")
	outDir := os.Getenv("PEEPWHO_PROBE_OUT")
	token := os.Getenv("PEEPWHO_GITHUB_TOKEN")
	if repoPath == "" || repoFull == "" || outDir == "" || token == "" {
		t.Skip("probe env not set")
	}

	git := connector.NewGitHistory(connector.GitOptions{Paths: []string{repoPath}, SinceDays: 730, Log: os.Stderr})
	gitRecs, err := git.Fetch(context.Background())
	if err != nil {
		t.Fatalf("git: %v", err)
	}

	pages := 10
	if v := os.Getenv("PEEPWHO_PROBE_PAGES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			pages = n
		}
	}
	gh := connector.NewGitHubWithClient(github.New(token, github.WithMaxPages(pages)),
		connector.GitHubOptions{
			Repos:    []string{repoFull},
			Since:    time.Now().AddDate(-1, 0, 0),
			Episodes: true,
			Log:      os.Stderr,
		})
	ghRecs, err := gh.Fetch(context.Background())
	if err != nil {
		t.Fatalf("github: %v", err)
	}
	t.Logf("git records: %d, github records: %d, pull dirs: %d, pull people: %d, episodes: %d",
		len(gitRecs), len(ghRecs), len(git.PullDirs()), len(gh.PullPeople()), len(gh.Episodes()))

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := SaveProbe(outDir+"/probe.json", Probe{
		PullDirs:   Keyed(git.PullDirs()),
		PullPeople: Keyed(gh.PullPeople()),
		Episodes:   gh.Episodes(),
	}); err != nil {
		t.Fatalf("save probe: %v", err)
	}

	ix := index.New()
	ix.Add(gitRecs)
	ix.Add(ghRecs)
	ix.AutoJoin()
	ix.Canonicalize()
	if err := ix.Save(outDir + "/index.json"); err != nil {
		t.Fatalf("save: %v", err)
	}
	t.Logf("saved merged index: %d people", len(ix.Graph.People))
}
