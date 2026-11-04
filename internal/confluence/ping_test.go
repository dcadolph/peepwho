package confluence

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dcadolph/peepwho/internal/httputil"
)

// TestPing verifies the auth check hits the current-user endpoint, returns nil
// on success, and a *httputil.StatusError carrying the code on bad credentials.
func TestPing(t *testing.T) {
	t.Parallel()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiBaseCloud+"/user/current" {
			t.Errorf("path = %q, want %q", r.URL.Path, apiBaseCloud+"/user/current")
		}
		io.WriteString(w, `{"accountId":"1","displayName":"Me"}`)
	}))
	t.Cleanup(ok.Close)
	if err := New(ok.URL, "e", "t").Ping(context.Background()); err != nil {
		t.Errorf("ping on 200: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(bad.Close)
	err := New(bad.URL, "e", "t").Ping(context.Background())
	var se *httputil.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusUnauthorized {
		t.Errorf("ping on 401 = %v, want StatusError 401", err)
	}
}
