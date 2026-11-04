package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newPromReplay serves the responses captured from api.github.com for
// prometheus/prometheus: the repo, its top hundred contributors, fifty closed
// pull requests, and fifty issues. The issue listing mixes real issues with
// pull requests, and the contributor list carries Bot-typed accounts, because
// that is what GitHub actually returns.
func newPromReplay(t *testing.T) *httptest.Server {
	t.Helper()
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read capture: %v", err)
		}
		return data
	}
	repo := read("prom_repo.json")
	contributors := read("prom_contributors.json")
	pulls := read("prom_pulls.json")
	issues := read("prom_issues.json")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Each capture is a single page; serving it without a Link header ends
		// pagination the way GitHub's own last page does.
		switch {
		case strings.HasSuffix(r.URL.Path, "/contributors"):
			_, _ = w.Write(contributors)
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_, _ = w.Write(pulls)
		case strings.HasSuffix(r.URL.Path, "/issues"):
			_, _ = w.Write(issues)
		case strings.HasSuffix(r.URL.Path, "/repos/prometheus/prometheus"):
			_, _ = w.Write(repo)
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestPromCapture runs the client over captured GitHub responses and holds
// the parse to what the real API returns.
func TestPromCapture(t *testing.T) {
	t.Parallel()
	srv := newPromReplay(t)
	t.Cleanup(srv.Close)
	c := New("test-token", WithBaseURL(srv.URL))
	ctx := context.Background()

	repo, err := c.Repo(ctx, "prometheus", "prometheus")
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if repo.Name != "prometheus" || len(repo.Topics) == 0 {
		t.Errorf("repo = %+v, want its name and topics parsed", repo)
	}

	contributors, err := c.Contributors(ctx, "prometheus", "prometheus")
	if err != nil {
		t.Fatalf("Contributors: %v", err)
	}
	if len(contributors) != 100 {
		t.Fatalf("contributors = %d, want the captured page", len(contributors))
	}
	bots := 0
	for _, con := range contributors {
		if con.Login == "" {
			t.Error("a contributor has no login")
		}
		if con.Contributions <= 0 {
			t.Errorf("%s has %d contributions", con.Login, con.Contributions)
		}
		if con.Type == "Bot" {
			bots++
		}
	}
	// The capture includes at least one Bot-typed account (dependabot); the
	// type must survive parsing or bot filtering upstream has nothing to act on.
	if bots == 0 {
		t.Error("no Bot-typed contributors parsed from a capture that has them")
	}

	pulls, err := c.PullRequests(ctx, "prometheus", "prometheus")
	if err != nil {
		t.Fatalf("PullRequests: %v", err)
	}
	if len(pulls) != 50 {
		t.Fatalf("pulls = %d, want the captured page", len(pulls))
	}
	merged := 0
	for _, pr := range pulls {
		if pr.Number <= 0 || pr.User.Login == "" {
			t.Errorf("pull %+v lacks number or author", pr)
		}
		if !pr.MergedAt.IsZero() {
			merged++
			if pr.MergedAt.After(time.Now().Add(24 * time.Hour)) {
				t.Errorf("pull %d merged in the future: %v", pr.Number, pr.MergedAt)
			}
		}
	}
	if merged < 20 {
		t.Errorf("merged pulls = %d, want most of a closed-PR capture", merged)
	}

	issues, err := c.Issues(ctx, "prometheus", "prometheus", time.Time{})
	if err != nil {
		t.Fatalf("Issues: %v", err)
	}
	if len(issues) != 50 {
		t.Fatalf("issues = %d, want the captured page", len(issues))
	}
	prsInIssues := 0
	for _, is := range issues {
		if is.IsPullRequest() {
			prsInIssues++
		}
	}
	// GitHub's issues endpoint returns pull requests mixed in; if this reads
	// zero, IsPullRequest broke, and every PR double-counts as an issue.
	if prsInIssues < 10 {
		t.Errorf("pull requests among issues = %d, want the capture's real mix", prsInIssues)
	}
	if prsInIssues == len(issues) {
		t.Error("every issue parsed as a pull request")
	}
}
