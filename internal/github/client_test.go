package github

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// TestRepo verifies repository metadata decodes, including topics.
func TestRepo(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"peepwho","full_name":"o/peepwho",`+
			`"description":"find people","topics":["search","people"]}`)
	}))
	t.Cleanup(srv.Close)
	repo, err := New("ghp-test", WithBaseURL(srv.URL)).Repo(context.Background(), "o", "peepwho")
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if repo.Name != "peepwho" || len(repo.Topics) != 2 {
		t.Errorf("repo = %+v", repo)
	}
}

// TestContributorsAndPulls verifies list endpoints and pull request helpers.
func TestContributorsAndPulls(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/contributors", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"login":"jane","contributions":50},{"login":"bob","contributions":3}]`)
	})
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"title":"fix retries","user":{"login":"jane"},`+
			`"labels":[{"name":"billing"}],"requested_reviewers":[{"login":"bob"}]}]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New("ghp-test", WithBaseURL(srv.URL))

	cons, err := c.Contributors(context.Background(), "o", "r")
	if err != nil || len(cons) != 2 || cons[0].Login != "jane" {
		t.Fatalf("contributors = %v, err %v", cons, err)
	}
	pulls, err := c.PullRequests(context.Background(), "o", "r")
	if err != nil || len(pulls) != 1 {
		t.Fatalf("pulls = %v, err %v", pulls, err)
	}
	pr := pulls[0]
	if pr.Author() != "jane" || !slices.Contains(pr.LabelNames(), "billing") ||
		!slices.Contains(pr.Reviewers(), "bob") {
		t.Errorf("pr helpers wrong: author=%q labels=%v reviewers=%v",
			pr.Author(), pr.LabelNames(), pr.Reviewers())
	}
}

// TestPagination verifies list endpoints follow the Link header across pages.
func TestPagination(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(nil)
	base := "http://" + srv.Listener.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/contributors", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			io.WriteString(w, `[{"login":"carl","contributions":1}]`)
			return
		}
		w.Header().Set("Link", "<"+base+"/repos/o/r/contributors?page=2>; rel=\"next\"")
		io.WriteString(w, `[{"login":"jane","contributions":50},{"login":"bob","contributions":3}]`)
	})
	srv.Config.Handler = mux
	srv.Start()
	t.Cleanup(srv.Close)

	cons, err := New("ghp-test", WithBaseURL(base)).Contributors(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("Contributors: %v", err)
	}
	if len(cons) != 3 || cons[2].Login != "carl" {
		t.Errorf("contributors = %+v, want all three across two pages", cons)
	}
}

// TestPaginationTruncationSignaled verifies a listing that never stops
// advertising a next page is reported as truncated, with the partial results
// still returned, rather than passed off as complete.
func TestPaginationTruncationSignaled(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(nil)
	base := "http://" + srv.Listener.Addr().String()
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", "<"+base+`/repos/o/r/contributors?page=2>; rel="next"`)
		io.WriteString(w, `[{"login":"jane","contributions":1}]`)
	})
	srv.Start()
	t.Cleanup(srv.Close)

	cons, err := New("ghp-test", WithBaseURL(base)).Contributors(context.Background(), "o", "r")
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if len(cons) == 0 {
		t.Error("truncation should still return the partial results")
	}
}

// TestPaginationStaysOnHost verifies a next link naming a foreign host is not
// followed, so the bearer token stays on the API host.
func TestPaginationStaysOnHost(t *testing.T) {
	t.Parallel()
	evil := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("foreign host was contacted")
	}))
	t.Cleanup(evil.Close)

	srv := httptest.NewUnstartedServer(nil)
	evilNext := evil.URL + "/steal"
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", "<"+evilNext+`>; rel="next"`)
		io.WriteString(w, `[{"login":"jane","contributions":1}]`)
	})
	srv.Start()
	t.Cleanup(srv.Close)

	cons, err := New("ghp-test", WithBaseURL(srv.URL)).Contributors(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("Contributors: %v", err)
	}
	if len(cons) != 1 {
		t.Errorf("contributors = %+v, want only the first page", cons)
	}
}

// TestFileContents verifies base64 file contents decode.
func TestFileContents(t *testing.T) {
	t.Parallel()
	encoded := base64.StdEncoding.EncodeToString([]byte("* @jane"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"encoding":"base64","content":"`+encoded+`"}`)
	}))
	t.Cleanup(srv.Close)
	got, err := New("ghp-test", WithBaseURL(srv.URL)).FileContents(context.Background(), "o", "r", "CODEOWNERS")
	if err != nil || string(got) != "* @jane" {
		t.Fatalf("contents = %q, err %v", got, err)
	}
}

// TestNotFound verifies a 404 maps to ErrNotFound.
func TestNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	_, err := New("ghp-test", WithBaseURL(srv.URL)).FileContents(context.Background(), "o", "r", "CODEOWNERS")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestRateLimited verifies an exhausted primary limit maps to ErrRateLimited.
func TestRateLimited(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "100")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	_, err := New("ghp-test", WithBaseURL(srv.URL)).Repo(context.Background(), "o", "r")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

// TestRetryAfter verifies a Retry-After response is retried and then succeeds.
func TestRetryAfter(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		i := calls
		calls++
		mu.Unlock()
		if i == 0 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{}`)
			return
		}
		io.WriteString(w, `{"name":"r"}`)
	}))
	t.Cleanup(srv.Close)
	repo, err := New("ghp-test", WithBaseURL(srv.URL)).Repo(context.Background(), "o", "r")
	if err != nil || repo.Name != "r" {
		t.Fatalf("repo = %v, err %v", repo, err)
	}
}

// TestNewEmptyTokenPanics verifies the constructor guards an empty token.
func TestNewEmptyTokenPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("New(\"\") did not panic")
		}
	}()
	New("")
}
