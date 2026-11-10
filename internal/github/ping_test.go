package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dcadolph/whotoask/internal/httputil"
)

// TestPing verifies the auth check hits the authenticated-user endpoint, returns
// nil on success, and a *httputil.StatusError carrying the code on a bad token.
func TestPing(t *testing.T) {
	t.Parallel()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("path = %q, want /user", r.URL.Path)
		}
		io.WriteString(w, `{"login":"me"}`)
	}))
	t.Cleanup(ok.Close)
	if err := New("t", WithBaseURL(ok.URL)).Ping(context.Background()); err != nil {
		t.Errorf("ping on 200: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(bad.Close)
	err := New("t", WithBaseURL(bad.URL)).Ping(context.Background())
	var se *httputil.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusUnauthorized {
		t.Errorf("ping on 401 = %v, want StatusError 401", err)
	}
}
