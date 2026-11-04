package connector

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/dcadolph/peepwho/internal/github"
)

// TestGitHubIncrementalSkipsSnapshots verifies an incremental GitHub read skips
// the whole-repo contributor and CODEOWNERS snapshots (whose weight would double
// if folded again), passes a since bound to the issues endpoint, and stops at
// pull requests older than the watermark, folding only the delta.
func TestGitHubIncrementalSkipsSnapshots(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	var calledContributors, calledCodeowners bool
	var issuesSince string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"r","full_name":"o/r","topics":["billing"]}`)
	})
	mux.HandleFunc("/repos/o/r/contributors", func(w http.ResponseWriter, _ *http.Request) {
		calledContributors = true
		io.WriteString(w, `[{"login":"oldtimer","contributions":100}]`)
	})
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, _ *http.Request) {
		// Newest-first: a recent PR after the watermark, then one before it.
		io.WriteString(w, `[{"title":"recent fix","user":{"login":"jane"},"labels":[{"name":"retries"}],`+
			`"updated_at":"2026-06-25T10:00:00Z"},`+
			`{"title":"ancient","user":{"login":"jane"},"labels":[{"name":"legacy"}],`+
			`"updated_at":"2026-01-01T10:00:00Z"}]`)
	})
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		issuesSince = r.URL.Query().Get("since")
		io.WriteString(w, `[{"user":{"login":"dan"},"labels":[{"name":"dashboard"}],`+
			`"title":"recent issue","updated_at":"2026-06-24T08:00:00Z"}]`)
	})
	mux.HandleFunc("/repos/o/r/contents/CODEOWNERS", func(w http.ResponseWriter, _ *http.Request) {
		calledCodeowners = true
		io.WriteString(w, `{"encoding":"base64","content":""}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := github.New("ghp-test", github.WithBaseURL(srv.URL))
	recs, err := NewGitHubWithClient(client, GitHubOptions{Repos: []string{"o/r"}, Since: since}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if calledContributors {
		t.Error("incremental run fetched the whole-repo contributors snapshot; it must be skipped")
	}
	if calledCodeowners {
		t.Error("incremental run fetched CODEOWNERS; it must be skipped")
	}
	if issuesSince == "" {
		t.Error("incremental run did not pass a since bound to the issues endpoint")
	}

	byID := make(map[string]Record)
	for _, r := range recs {
		byID[r.PersonID] = r
	}
	if jane := byID["github:jane"]; !slices.Contains(jane.Topics, "retries") {
		t.Errorf("recent PR author jane topics = %v, want retries", jane.Topics)
	}
	if jane := byID["github:jane"]; slices.Contains(jane.Topics, "legacy") {
		t.Error("a pre-watermark PR was folded; the newest-first break should have stopped it")
	}
	if _, ok := byID["github:oldtimer"]; ok {
		t.Error("a contributor-only login appeared though contributors was skipped")
	}
	if dan := byID["github:dan"]; !slices.Contains(dan.Topics, "dashboard") {
		t.Errorf("recent issue author dan topics = %v, want dashboard", dan.Topics)
	}
}
