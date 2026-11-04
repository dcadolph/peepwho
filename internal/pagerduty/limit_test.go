package pagerduty

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPrimaryCallsSurfaceRateLimits checks a rate limit on the calls that fetch
// data comes back as itself. Mapped to a generic failure it reads as an empty
// source, and a whole on-call roster drops out of the index without a word.
func TestPrimaryCallsSurfaceRateLimits(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := New("token", WithBaseURL(srv.URL))
	if _, err := c.Services(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Errorf("Services = %v, want ErrRateLimited", err)
	}
	if _, err := c.OnCalls(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Errorf("OnCalls = %v, want ErrRateLimited", err)
	}
}
