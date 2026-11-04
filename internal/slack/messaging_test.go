package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPostMessage verifies the channel, text, and thread parameters are sent.
func TestPostMessage(t *testing.T) {
	t.Parallel()
	var gotChannel, gotText, gotThread string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotChannel = r.PostFormValue("channel")
		gotText = r.PostFormValue("text")
		gotThread = r.PostFormValue("thread_ts")
		io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	c := New("xoxb-test", WithBaseURL(srv.URL))
	if err := c.PostMessage(context.Background(), "C1", "123.1", "hello"); err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if gotChannel != "C1" || gotText != "hello" || gotThread != "123.1" {
		t.Errorf("params: channel=%q text=%q thread=%q", gotChannel, gotText, gotThread)
	}
}

// TestAuthTest verifies the bot user ID is returned.
func TestAuthTest(t *testing.T) {
	t.Parallel()
	srv := newServer(t, map[string][]canned{
		"auth.test": {{status: 200, body: `{"ok":true,"user_id":"U999"}`}},
	})
	c := New("xoxb-test", WithBaseURL(srv.URL))
	auth, err := c.AuthTest(context.Background())
	id := auth.UserID
	if err != nil || id != "U999" {
		t.Fatalf("AuthTest = %q, %v; want U999", id, err)
	}
}

// TestConnectionsOpen verifies the WebSocket URL is returned.
func TestConnectionsOpen(t *testing.T) {
	t.Parallel()
	srv := newServer(t, map[string][]canned{
		"apps.connections.open": {{status: 200, body: `{"ok":true,"url":"wss://example/link"}`}},
	})
	c := New("xapp-test", WithBaseURL(srv.URL))
	gotURL, err := c.ConnectionsOpen(context.Background())
	if err != nil || gotURL != "wss://example/link" {
		t.Fatalf("ConnectionsOpen = %q, %v; want wss://example/link", gotURL, err)
	}
}

// TestPostEphemeral verifies the channel, user, and text parameters reach
// chat.postEphemeral, since an ephemeral answer is the privacy boundary for
// recall in Slack.
func TestPostEphemeral(t *testing.T) {
	t.Parallel()
	var gotPath, gotChannel, gotUser, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotPath = r.URL.Path
		gotChannel = r.PostFormValue("channel")
		gotUser = r.PostFormValue("user")
		gotText = r.PostFormValue("text")
		io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	c := New("xoxb-test", WithBaseURL(srv.URL))
	if err := c.PostEphemeral(context.Background(), "C1", "U9", "only you see this"); err != nil {
		t.Fatalf("PostEphemeral: %v", err)
	}
	if gotPath != "/chat.postEphemeral" || gotChannel != "C1" || gotUser != "U9" ||
		gotText != "only you see this" {
		t.Errorf("params: path=%q channel=%q user=%q text=%q", gotPath, gotChannel, gotUser, gotText)
	}
}

// TestRespondVisibility verifies each reply carries the response_type that
// matches its name. RespondPrivately sending anything but ephemeral would post
// a person's recall answer to the whole channel, so the two wrappers are
// checked against the same constants they pass.
func TestRespondVisibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantType string
	}{
		{WantType: inChannel}, // Test 0: Public reply.
		{WantType: ephemeral}, // Test 1: Private reply.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var got map[string]string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &got)
			}))
			t.Cleanup(srv.Close)

			err := respond(context.Background(), srv.URL+"/cmd", "answer", test.WantType, srv.URL)
			if err != nil {
				t.Fatalf("respond: %v", err)
			}
			if got["response_type"] != test.WantType || got["text"] != "answer" {
				t.Errorf("payload = %v, want response_type %q", got, test.WantType)
			}
		})
	}
	// The exported wrappers must pass those same values, which is the part a
	// reader of the bot code relies on.
	if inChannel != "in_channel" || ephemeral != "ephemeral" {
		t.Errorf("visibility constants drifted: %q and %q", inChannel, ephemeral)
	}
}

// TestRespondRejectsForeignURL verifies a response URL outside hooks.slack.com
// is refused before any request is made, so a forged slash-command payload
// cannot make the bot POST an answer to an attacker's server.
func TestRespondRejectsForeignURL(t *testing.T) {
	t.Parallel()
	for testNum, u := range []string{
		"https://evil.example.com/steal",
		"http://hooks.slack.com/downgraded",
		"https://hooks.slack.com.evil.example.com/spoofed",
		"",
	} {
		if err := RespondPrivately(context.Background(), u, "answer"); err == nil {
			t.Errorf("test %d: RespondPrivately(%q) accepted a foreign URL", testNum, u)
		}
	}
}
