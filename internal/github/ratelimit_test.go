package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPrimaryRateLimitIsReportedNotRetried checks an exhausted primary quota is
// surfaced at once, with the time it resets, rather than retried. A primary
// limit resets on a fixed clock that can be an hour away, so every retry fails
// exactly as the first did: the waiting bought nothing and the caller ended up
// with a generic error instead of the one fact they can act on.
func TestPrimaryRateLimitIsReportedNotRetried(t *testing.T) {
	t.Parallel()
	reset := time.Now().Add(42 * time.Minute).Unix()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := New("token", WithBaseURL(srv.URL))
	_, err := c.OrgRepos(context.Background(), "acme")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
	if calls != 1 {
		t.Errorf("made %d requests, want exactly one: retrying a primary limit cannot succeed", calls)
	}
	if !strings.Contains(err.Error(), "resets at") {
		t.Errorf("error = %q, want it to say when the quota resets", err)
	}
}

// TestSecondaryRateLimitIsRetried is the other half: a limit GitHub says to
// wait out is waited out rather than surfaced, since that one does succeed.
func TestSecondaryRateLimitIsRetried(t *testing.T) {
	t.Parallel()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	c := New("token", WithBaseURL(srv.URL))
	if _, err := c.OrgRepos(context.Background(), "acme"); err != nil {
		t.Fatalf("org repos: %v", err)
	}
	if calls != 2 {
		t.Errorf("made %d requests, want the first retried after the wait GitHub asked for", calls)
	}
}
