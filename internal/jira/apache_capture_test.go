package jira

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newApacheReplay serves the search responses captured from Apache's public
// Jira Server (issues.apache.org, project KAFKA, resolved issues by updated
// desc, two pages of 50). A hand-written fixture agrees with its author;
// these bytes are what a real Server deployment actually returns, deleted
// accounts, missing emails, CRLF descriptions and all.
func newApacheReplay(t *testing.T) *httptest.Server {
	t.Helper()
	page := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read capture: %v", err)
		}
		return data
	}
	page1, page2 := page("apache_search_page1.json"), page("apache_search_page2.json")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/search") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("startAt") {
		case "", "0":
			_, _ = w.Write(page1)
		case "50":
			_, _ = w.Write(page2)
		default:
			_, _ = w.Write([]byte(`{"issues":[],"startAt":100,"total":14514}`))
		}
	}))
}

// TestSearchApacheCapture runs the Server client over captured Apache Jira
// responses and holds the parse to what the real data contains. Every
// assertion here is a property of the capture, not of a fixture written to
// satisfy the parser.
func TestSearchApacheCapture(t *testing.T) {
	t.Parallel()
	srv := newApacheReplay(t)
	t.Cleanup(srv.Close)

	c := NewServer(srv.URL, "")
	issues, err := c.Search(context.Background(), "project=KAFKA", 100)
	// The capture's tracker holds 14,514 matching issues, so a 100-issue read
	// is truncated by definition, and the client must say so with the exact
	// remainder rather than logging the read as complete.
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("Search err = %v, want ErrTruncated: the capture holds 14514 matches", err)
	}
	if !strings.Contains(err.Error(), "100 of 14514") {
		t.Errorf("truncation error = %q, want the exact 100 of 14514 remainder", err)
	}
	if len(issues) != 100 {
		t.Fatalf("issues = %d, want both 50-issue pages returned alongside the truncation", len(issues))
	}

	assignees := make(map[string]bool)
	commentAuthors := make(map[string]bool)
	described := 0
	for _, is := range issues {
		if !strings.HasPrefix(is.Key, "KAFKA-") {
			t.Errorf("key %q is not a KAFKA issue", is.Key)
		}
		if is.Fields.Summary == "" {
			t.Errorf("%s has no summary", is.Key)
		}
		// The capture's JQL selected resolved issues only, so a single false
		// here means resolutiondate or status parsing broke, the failure that
		// silently stops every issue from becoming an episode.
		if !is.Resolved() {
			t.Errorf("%s parsed as unresolved", is.Key)
		}
		if is.Fields.Updated == "" {
			t.Errorf("%s has no updated time", is.Key)
		} else if _, err := time.Parse("2006-01-02T15:04:05.000-0700", is.Fields.Updated); err != nil {
			t.Errorf("%s updated %q does not parse: %v", is.Key, is.Fields.Updated, err)
		}
		if a := is.Fields.Assignee; a != nil {
			// Server users carry no email, so Identity must fall back to the
			// username; an empty identity would index work under nobody.
			if a.Identity() == "" {
				t.Errorf("%s assignee %q has empty identity", is.Key, a.DisplayName)
			}
			assignees[a.Identity()] = true
		}
		for _, u := range is.CommentAuthors() {
			if u.Identity() == "" {
				t.Errorf("%s comment author %q has empty identity", is.Key, u.DisplayName)
			}
			commentAuthors[u.Identity()] = true
		}
		if d := is.Description(); d != "" {
			described++
			// Server descriptions are plain strings, often with CRLF line
			// endings; the extractor must return readable text, not raw JSON.
			if strings.Contains(d, `{"type":`) || strings.Contains(d, "rich_text") {
				t.Errorf("%s description reads as markup: %.80q", is.Key, d)
			}
		}
	}
	// Properties of the captured window, generous enough to survive a
	// re-capture of any 100 resolved KAFKA issues.
	if len(assignees) < 20 {
		t.Errorf("distinct assignees = %d, want a real spread of people", len(assignees))
	}
	if len(commentAuthors) < 20 {
		t.Errorf("distinct comment authors = %d, want a real spread", len(commentAuthors))
	}
	if described < 50 {
		t.Errorf("issues with descriptions = %d, want most of the capture", described)
	}
}
