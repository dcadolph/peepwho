package connector

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dcadolph/whotoask/internal/slack"
)

// TestSlackOldestSince verifies the history window floor is used by default and
// a more recent watermark wins, so an incremental read narrows to new messages
// while a stale cursor never widens the read past the window.
func TestSlackOldestSince(t *testing.T) {
	t.Parallel()
	recent := time.Now().Add(-24 * time.Hour)
	if got := slackOldestSince(180, recent); got != slackTS(recent) {
		t.Errorf("recent watermark: oldest = %s, want %s", got, slackTS(recent))
	}
	stale := time.Now().AddDate(-2, 0, 0)
	if got := slackOldestSince(180, stale); got == slackTS(stale) {
		t.Errorf("stale watermark used directly: %s; want the window floor", got)
	}
	if got := slackOldestSince(180, time.Time{}); got == "" || got == slackTS(time.Time{}) {
		t.Errorf("zero watermark: oldest = %q, want the window floor", got)
	}
}

// TestSlackFetchUsesWatermarkOldest verifies an incremental Slack read passes the
// watermark to conversations.history as oldest, while the identity-only users
// list is still read so message authors resolve to names.
func TestSlackFetchUsesWatermarkOldest(t *testing.T) {
	t.Parallel()
	since := time.Now().Add(-48 * time.Hour)
	var gotOldest string
	mux := http.NewServeMux()
	mux.HandleFunc("/users.list", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"members":[{"id":"U1","profile":{"real_name":"Jane","email":"jane@x.com"}}]}`)
	})
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"channels":[{"id":"C1","name":"billing","purpose":{"value":"billing"}}]}`)
	})
	mux.HandleFunc("/conversations.history", func(w http.ResponseWriter, r *http.Request) {
		gotOldest = r.FormValue("oldest")
		io.WriteString(w, `{"ok":true,"has_more":false,"messages":[`+
			`{"type":"message","user":"U1","text":"retries fixed","ts":"9999999999.000100"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := slack.New("xoxb-test", slack.WithBaseURL(srv.URL))
	recs, err := NewSlackWithClient(client, SlackOptions{Since: since}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotOldest != slackTS(since) {
		t.Errorf("history oldest = %q, want the watermark %q", gotOldest, slackTS(since))
	}
	var sawJane bool
	for _, r := range recs {
		if r.Email == "jane@x.com" {
			sawJane = true
		}
	}
	if !sawJane {
		t.Error("jane missing from an incremental Slack fetch; users.list must still be read")
	}
}
