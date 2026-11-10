package connector

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dcadolph/whotoask/internal/github"
)

// TestGitHubFetch verifies topics come from repo metadata, PR labels and titles,
// reviewers and assignees, non-PR issues, and CODEOWNERS, and that pull requests
// returned by the issues endpoint are skipped.
func TestGitHubFetch(t *testing.T) {
	t.Parallel()
	owners := base64.StdEncoding.EncodeToString([]byte("/internal/ @kim"))
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"billing-service","full_name":"o/r",`+
			`"description":"Wiz scanning integration","topics":["billing"]}`)
	})
	mux.HandleFunc("/repos/o/r/contributors", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"login":"jane","contributions":10}]`)
	})
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"title":"Fix wiz scan flakiness","user":{"login":"jane"},`+
			`"labels":[{"name":"retries"}],"requested_reviewers":[{"login":"bob"}],`+
			`"assignees":[{"login":"carol"}],"updated_at":"2026-07-01T10:00:00Z"}]`)
	})
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"user":{"login":"dan"},"labels":[{"name":"dashboard"}],`+
			`"title":"Wiz dashboard broken","updated_at":"2026-06-15T08:00:00Z"},`+
			`{"user":{"login":"ghost"},"labels":[{"name":"shouldskip"}],"title":"x",`+
			`"pull_request":{"url":"y"}}]`)
	})
	mux.HandleFunc("/repos/o/r/contents/CODEOWNERS", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"encoding":"base64","content":"`+owners+`"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := github.New("ghp-test", github.WithBaseURL(srv.URL))
	recs, err := NewGitHubWithClient(client, GitHubOptions{Repos: []string{"o/r"}}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	byID := make(map[string]Record)
	for _, r := range recs {
		byID[r.PersonID] = r
	}

	// Topics carries what a repo, pull request, or issue stated as a topic or
	// label; title words arrive as weak topics. Both count toward affinity, so
	// check the union for presence and the split for which side each landed on.
	all := func(r Record) []string {
		return append(append([]string(nil), r.Topics...), r.WeakTopics...)
	}
	if jane := byID["github:jane"]; !slices.Contains(all(jane), "wiz") ||
		!slices.Contains(all(jane), "scan") || !slices.Contains(all(jane), "retries") {
		t.Errorf("jane topics = %v, weak = %v, want wiz, scan, retries",
			jane.Topics, jane.WeakTopics)
	}
	if want := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC); !byID["github:jane"].Time.Equal(want) {
		t.Errorf("jane time = %v, want the PR update time %v", byID["github:jane"].Time, want)
	}
	if want := time.Date(2026, 6, 15, 8, 0, 0, 0, time.UTC); !byID["github:dan"].Time.Equal(want) {
		t.Errorf("dan time = %v, want the issue update time %v", byID["github:dan"].Time, want)
	}
	if bob := byID["github:bob"]; !slices.Contains(all(bob), "wiz") {
		t.Errorf("reviewer bob topics = %v, weak = %v, want wiz", bob.Topics, bob.WeakTopics)
	}
	if carol := byID["github:carol"]; !slices.Contains(all(carol), "wiz") {
		t.Errorf("assignee carol topics = %v, weak = %v, want wiz", carol.Topics, carol.WeakTopics)
	}
	dan := byID["github:dan"]
	if !slices.Contains(all(dan), "dashboard") || !slices.Contains(all(dan), "wiz") {
		t.Errorf("issue author dan topics = %v, weak = %v, want dashboard, wiz",
			dan.Topics, dan.WeakTopics)
	}
	// The dashboard label was stated by the issue; wiz only appears in its title.
	if !slices.Contains(dan.Topics, "dashboard") {
		t.Errorf("dan curated topics = %v, want the dashboard label", dan.Topics)
	}
	if !slices.Contains(dan.WeakTopics, "wiz") {
		t.Errorf("dan weak topics = %v, want the title word wiz", dan.WeakTopics)
	}
	if _, ok := byID["github:ghost"]; ok {
		t.Error("pull request returned by the issues endpoint should be skipped")
	}
	// A repo's own CODEOWNERS @login remaps into the github namespace, so it
	// merges with that login's pull request and issue activity.
	if kim := byID["github:kim"]; kim.Name != "@kim" {
		t.Errorf("codeowners record = %+v, want @kim under github:kim", kim)
	}
	if _, ok := byID["codeowners:kim"]; ok {
		t.Error("a repo's own CODEOWNERS @login should not stay in the codeowners namespace")
	}
}

// TestGitHubSelfManagedPullCountsOnce pins the double-count fix: an author
// who is also assigned to their own pull request and comments on it bumped
// every token up to three times, so self-managed work outweighed the same
// work reviewed by others.
func TestGitHubSelfManagedPullCountsOnce(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"r","full_name":"o/r"}`)
	})
	mux.HandleFunc("/repos/o/r/contributors", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"number":7,"title":"x","user":{"login":"jane"},`+
			`"assignees":[{"login":"jane"}],"labels":[{"name":"retries"}],`+
			`"merged_at":"2026-07-01T09:00:00Z","updated_at":"2026-07-01T10:00:00Z"}]`)
	})
	mux.HandleFunc("/repos/o/r/pulls/7/reviews", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"user":{"login":"bob"}}]`)
	})
	mux.HandleFunc("/repos/o/r/pulls/7/comments", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"user":{"login":"jane"}}]`)
	})
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"user":{"login":"dan"},"assignees":[{"login":"dan"}],`+
			`"labels":[{"name":"dashboard"}],"title":"y","updated_at":"2026-06-15T08:00:00Z"}]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := github.New("ghp-test", github.WithBaseURL(srv.URL))
	src := NewGitHubWithClient(client, GitHubOptions{Repos: []string{"o/r"}})
	recs, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	counts := map[string]int{}
	for _, r := range recs {
		if r.Kind == KindPerson {
			counts[r.PersonID] = countTokens(r, "retries") + countTokens(r, "dashboard")
		}
	}
	if counts["github:jane"] != 1 {
		t.Errorf("jane retries count = %d, want 1: author, assignee, and commenter on one "+
			"pull request is one person", counts["github:jane"])
	}
	if counts["github:bob"] != 1 {
		t.Errorf("reviewer bob retries count = %d, want 1", counts["github:bob"])
	}
	if counts["github:dan"] != 1 {
		t.Errorf("dan dashboard count = %d, want 1: a self-assigned issue is one piece of work",
			counts["github:dan"])
	}
	// The participant list still carries everyone once for review credit.
	people := src.PullPeople()[PullRef{Repo: "o/r", Number: 7}]
	if len(people) != 2 || !slices.Contains(people, "jane") || !slices.Contains(people, "bob") {
		t.Errorf("pull people = %v, want jane and bob once each", people)
	}
}

// countTokens returns how many times token appears across a record's strong
// and weak topics.
func countTokens(r Record, token string) int {
	n := 0
	for _, t := range append(append([]string(nil), r.Topics...), r.WeakTopics...) {
		if t == token {
			n++
		}
	}
	return n
}

// TestGitHubSecondFetchDropsStaleParticipants verifies Fetch starts from a
// clean participant map: entries kept from an earlier Fetch would collect
// review credit for pull requests the current fetch never saw.
func TestGitHubSecondFetchDropsStaleParticipants(t *testing.T) {
	t.Parallel()
	var pullCalls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"r","full_name":"o/r"}`)
	})
	mux.HandleFunc("/repos/o/r/contributors", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, _ *http.Request) {
		if pullCalls.Add(1) == 1 {
			io.WriteString(w, `[{"number":5,"title":"x","user":{"login":"jane"},`+
				`"updated_at":"2026-07-01T10:00:00Z"}]`)
			return
		}
		io.WriteString(w, `[]`)
	})
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	src := NewGitHubWithClient(github.New("t", github.WithBaseURL(srv.URL)),
		GitHubOptions{Repos: []string{"o/r"}})
	if _, err := src.Fetch(context.Background()); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if len(src.PullPeople()) != 1 {
		t.Fatalf("pull people after first fetch = %v, want the one pull request", src.PullPeople())
	}
	if _, err := src.Fetch(context.Background()); err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if got := src.PullPeople(); len(got) != 0 {
		t.Errorf("pull people after second fetch = %v, want none: the pull request was not seen again", got)
	}
}

// TestGitHubReportsMalformedRepoFlag verifies a --github-repo value that is
// not owner/name is reported by name rather than silently dropped from the run.
func TestGitHubReportsMalformedRepoFlag(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/good", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"good","full_name":"o/good"}`)
	})
	mux.HandleFunc("/repos/o/good/contributors", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"login":"jane","contributions":5}]`)
	})
	mux.HandleFunc("/repos/o/good/pulls", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	mux.HandleFunc("/repos/o/good/issues", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var log strings.Builder
	src := NewGitHubWithClient(github.New("t", github.WithBaseURL(srv.URL)),
		GitHubOptions{Repos: []string{"acmeplatform", "o/good"}, Log: &log})
	recs, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(log.String(), `"acmeplatform" is not owner/name`) {
		t.Errorf("log does not name the malformed repo; log:\n%s", log.String())
	}
	found := false
	for _, r := range recs {
		if r.PersonID == "github:jane" {
			found = true
		}
	}
	if !found {
		t.Error("the well-formed repo was not indexed alongside the malformed entry")
	}
}

// TestGitHubReportsUnreadableCodeowners verifies a CODEOWNERS fetch that fails
// for any reason besides absence is reported: a rate limit or server error
// silently cost the run every declared owner.
func TestGitHubReportsUnreadableCodeowners(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"r","full_name":"o/r"}`)
	})
	mux.HandleFunc("/repos/o/r/contributors", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"login":"jane","contributions":5}]`)
	})
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	mux.HandleFunc("/repos/o/r/contents/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var log strings.Builder
	src := NewGitHubWithClient(github.New("t", github.WithBaseURL(srv.URL)),
		GitHubOptions{Repos: []string{"o/r"}, Log: &log})
	if _, err := src.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: an unreadable CODEOWNERS must not fail the repo: %v", err)
	}
	if !strings.Contains(log.String(), "CODEOWNERS unreadable") {
		t.Errorf("log does not report the unreadable CODEOWNERS; log:\n%s", log.String())
	}
}

// TestGitHubFetchSkipsBadRepo verifies one failing repository is logged and
// skipped rather than discarding every other repository's data.
func TestGitHubFetchSkipsBadRepo(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	// The good repo answers every endpoint.
	mux.HandleFunc("/repos/o/good", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"good","full_name":"o/good","topics":["billing"]}`)
	})
	mux.HandleFunc("/repos/o/good/contributors", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"login":"jane","contributions":5}]`)
	})
	mux.HandleFunc("/repos/o/good/pulls", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	mux.HandleFunc("/repos/o/good/issues", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	// The bad repo fails on its metadata endpoint.
	mux.HandleFunc("/repos/o/bad", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var logBuf strings.Builder
	client := github.New("ghp-test", github.WithBaseURL(srv.URL))
	recs, err := NewGitHubWithClient(client, GitHubOptions{
		Repos: []string{"o/bad", "o/good"}, Log: &logBuf,
	}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: one bad repo should not fail the run: %v", err)
	}

	var foundJane bool
	for _, r := range recs {
		if r.PersonID == "github:jane" {
			foundJane = true
		}
	}
	if !foundJane {
		t.Error("good repo's contributor was dropped when another repo failed")
	}
	if !strings.Contains(logBuf.String(), "skipping o/bad") {
		t.Errorf("expected a skip warning for o/bad, log = %q", logBuf.String())
	}
}

// TestGitHubResolvesEmailsConcurrently verifies email resolution fetches many
// contributor profiles concurrently and correctly, without dropping or racing
// on any of them. Against a real active org this is hundreds of profile
// requests, and doing them serially once made an org index time out.
func TestGitHubResolvesEmailsConcurrently(t *testing.T) {
	t.Parallel()
	const contributors = 200
	var accountHits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/billing", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"billing","full_name":"acme/billing"}`)
	})
	mux.HandleFunc("/repos/acme/billing/contributors", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("["))
		for i := range contributors {
			if i > 0 {
				w.Write([]byte(","))
			}
			fmt.Fprintf(w, `{"login":"dev%d","contributions":%d}`, i, i+1)
		}
		w.Write([]byte("]"))
	})
	mux.HandleFunc("/repos/acme/billing/pulls", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "[]") })
	mux.HandleFunc("/repos/acme/billing/issues", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "[]") })
	// One profile endpoint per contributor, each returning that dev's email.
	mux.HandleFunc("/users/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&accountHits, 1)
		login := strings.TrimPrefix(r.URL.Path, "/users/")
		fmt.Fprintf(w, `{"login":%q,"email":%q}`, login, login+"@x.com")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := github.New("token", github.WithBaseURL(srv.URL))
	recs, err := NewGitHubWithClient(client,
		GitHubOptions{Repos: []string{"acme/billing"}, ResolveEmails: true}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := atomic.LoadInt64(&accountHits); got != contributors {
		t.Errorf("resolved %d profiles, want one per contributor (%d)", got, contributors)
	}
	withEmail := 0
	for _, r := range recs {
		if strings.HasSuffix(r.Email, "@x.com") {
			withEmail++
		}
	}
	if withEmail != contributors {
		t.Errorf("%d records got an email, want all %d resolved", withEmail, contributors)
	}
}

// TestGitHubFetchesReposConcurrently verifies several repositories are indexed
// in parallel and every one's people and topics land, with no race on the
// shared tallies or the log. An org is many repos, so this is the common shape.
func TestGitHubFetchesReposConcurrently(t *testing.T) {
	t.Parallel()
	repos := []string{"acme/billing", "acme/payments", "acme/ledger", "acme/invoicing", "acme/refunds"}
	mux := http.NewServeMux()
	for _, full := range repos {
		full := full
		name := strings.SplitN(full, "/", 2)[1]
		mux.HandleFunc("/repos/"+full, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, `{"name":%q,"full_name":%q}`, name, full)
		})
		mux.HandleFunc("/repos/"+full+"/contributors", func(w http.ResponseWriter, _ *http.Request) {
			// A contributor unique to this repo, so the merge is observable.
			fmt.Fprintf(w, `[{"login":"dev-%s","contributions":5}]`, name)
		})
		mux.HandleFunc("/repos/"+full+"/pulls", func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, "[]")
		})
		mux.HandleFunc("/repos/"+full+"/issues", func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, "[]")
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var log strings.Builder
	client := github.New("token", github.WithBaseURL(srv.URL))
	src := NewGitHubWithClient(client, GitHubOptions{Repos: repos, Episodes: true, Log: &log})
	recs, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Episodes append to a shared slice from each repo goroutine, so this also
	// guards that concurrent path under the race detector.
	_ = src.Episodes()
	got := make(map[string]bool)
	for _, r := range recs {
		got[r.PersonID] = true
	}
	for _, full := range repos {
		name := strings.SplitN(full, "/", 2)[1]
		if !got["github:dev-"+name] {
			t.Errorf("contributor from %s missing; a concurrent repo was dropped", full)
		}
	}
	// Every repo logged its line, none interleaved into corruption.
	if n := strings.Count(log.String(), "github: indexed"); n != len(repos) {
		t.Errorf("logged %d indexed lines, want %d", n, len(repos))
	}
}

// TestGitHubFindsSubjectsWorkedTogether verifies that pull requests labeled with
// two subjects at once tie those subjects to each other, and name the person who
// worked across them. Labels are the only thing paired: a title is prose.
func TestGitHubFindsSubjectsWorkedTogether(t *testing.T) {
	t.Parallel()
	var pulls []string
	// Enough separate pull requests for the pairing to clear the floor below
	// which it would be read as coincidence.
	for i := range 6 {
		pulls = append(pulls, fmt.Sprintf(
			`{"title":"reconcile ledger %d","user":{"login":"jane"},`+
				`"labels":[{"name":"billing"},{"name":"ledger"}],`+
				`"updated_at":"2026-07-0%dT10:00:00Z"}`, i, i+1))
	}
	// Other work so neither subject is most of what the repository does, which
	// would mark it as scaffolding rather than a subject.
	for i := range 8 {
		pulls = append(pulls, fmt.Sprintf(
			`{"title":"unrelated %d","user":{"login":"bob"},`+
				`"labels":[{"name":"search"},{"name":"indexing"}],`+
				`"updated_at":"2026-06-0%dT10:00:00Z"}`, i, i%9+1))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"r","full_name":"o/r","topics":[]}`)
	})
	mux.HandleFunc("/repos/o/r/contributors", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "["+strings.Join(pulls, ",")+"]")
	})
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := github.New("ghp-test", github.WithBaseURL(srv.URL))
	recs, err := NewGitHubWithClient(client, GitHubOptions{Repos: []string{"o/r"}}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	var link TopicLink
	found := false
	for _, r := range recs {
		if r.Kind != KindTopic || r.Name != "billing" {
			continue
		}
		if r.Source != "github" {
			t.Errorf("source = %q, want github", r.Source)
		}
		for _, l := range r.Links {
			if l.To == "ledger" {
				link, found = l, true
			}
			if l.To == "search" {
				t.Error("billing was tied to search, which no pull request named alongside it")
			}
		}
	}
	if !found {
		t.Fatal("no pull request tied billing to ledger, though six named both")
	}
	if link.Witnesses != 1 || link.Sole != "jane" {
		t.Errorf("billing to ledger: %d people, sole %q; want the one who did that work",
			link.Witnesses, link.Sole)
	}
}
