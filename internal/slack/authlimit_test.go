package slack

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPrimaryCallsSurfaceAuthAndRateLimits covers what the two failures a real
// deployment actually hits look like on the calls that fetch data, rather than
// only on the ping. A token that has expired or lost a scope, and a workspace
// that has had enough for now, must each come back as themselves: mapped to a
// generic failure they read as "this source is empty", and a whole workspace
// silently drops out of the index.
func TestPrimaryCallsSurfaceAuthAndRateLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name   string
		Status int
		Body   string
		Want   error
	}{{ // Test 0: The token is no longer good.
		Name: "invalid auth", Status: http.StatusOK,
		Body: `{"ok":false,"error":"invalid_auth"}`, Want: ErrAPI,
	}, { // Test 1: The token lost the scope this call needs.
		Name: "missing scope", Status: http.StatusOK,
		Body: `{"ok":false,"error":"missing_scope"}`, Want: ErrAPI,
	}, { // Test 2: Too many requests, with no wait offered.
		Name: "rate limited", Status: http.StatusTooManyRequests, Body: ``, Want: ErrRateLimited,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Ask for no wait, so the retry path is exercised without the
				// test sitting through a real backoff.
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(test.Status)
				fmt.Fprint(w, test.Body)
			}))
			defer srv.Close()

			c := New("xoxb-test", WithBaseURL(srv.URL))
			if _, err := c.Users(context.Background()); !errors.Is(err, test.Want) {
				t.Errorf("Users = %v, want %v", err, test.Want)
			}
			if _, err := c.Channels(context.Background(), "public_channel"); !errors.Is(err, test.Want) {
				t.Errorf("Channels = %v, want %v", err, test.Want)
			}
		})
	}
}
