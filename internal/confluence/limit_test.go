package confluence

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPagesSurfacesRateLimit checks a rate limit on the call that fetches pages
// comes back as itself rather than as a generic failure. A generic failure
// reads as a space with nothing in it, and the whole wiki drops out of the
// index without saying why.
func TestPagesSurfacesRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := New(srv.URL, "someone@corp.com", "token")
	if _, err := c.Pages(context.Background(), Query{Spaces: []string{"ENG"}, Max: 10}); !errors.Is(err, ErrRateLimited) {
		t.Errorf("Pages = %v, want ErrRateLimited", err)
	}
}
