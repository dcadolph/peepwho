package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPingAuthTest verifies the auth check reaches auth.test, returns the bot
// user id on success, and maps a logical failure to ErrAPI, which is what the
// connect wizard relies on to detect a rejected Slack token.
func TestPingAuthTest(t *testing.T) {
	t.Parallel()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/auth.test") {
			t.Errorf("path = %q, want auth.test", r.URL.Path)
		}
		io.WriteString(w, `{"ok":true,"user_id":"U1","url":"https://acme.slack.com/","team":"Acme"}`)
	}))
	t.Cleanup(ok.Close)
	auth, err := New("t", WithBaseURL(ok.URL)).AuthTest(context.Background())
	if err != nil || auth.UserID != "U1" {
		t.Errorf("AuthTest on ok = (%+v, %v), want UserID U1", auth, err)
	}
	if auth.URL != "https://acme.slack.com/" || auth.Team != "Acme" {
		t.Errorf("AuthTest workspace = (%q, %q), want (https://acme.slack.com/, Acme)", auth.URL, auth.Team)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
	}))
	t.Cleanup(bad.Close)
	if _, err := New("t", WithBaseURL(bad.URL)).AuthTest(context.Background()); !errors.Is(err, ErrAPI) {
		t.Errorf("AuthTest on invalid_auth = %v, want ErrAPI", err)
	}
}
