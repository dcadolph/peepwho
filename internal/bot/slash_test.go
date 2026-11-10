package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// chanResponder signals slash answers on a channel.
type chanResponder struct {
	ch chan string
}

// Respond pushes the answer text onto the channel.
func (c chanResponder) Respond(_ context.Context, _, text string) error {
	c.ch <- text
	return nil
}

// TestRouteSlash verifies a slash command is answered through the response
// URL and an empty question gets the usage line.
func TestRouteSlash(t *testing.T) {
	t.Parallel()
	ch := make(chan string, 1)
	cmd := slashCommand{Text: "billing", UserID: "U2", ResponseURL: "https://hooks.slack.com/T"}
	routeSlash(context.Background(), okEngine(), chanResponder{ch: ch}, cmd, io.Discard)
	if txt := <-ch; !strings.Contains(txt, "Jane Roe") {
		t.Errorf("answer missing content: %s", txt)
	}

	cmd.Text = "   "
	routeSlash(context.Background(), okEngine(), chanResponder{ch: ch}, cmd, io.Discard)
	if txt := <-ch; txt != slashUsage {
		t.Errorf("empty question answer = %q, want usage", txt)
	}
}

// TestSlashHandlerDispatches verifies a signed slash POST is acknowledged and
// answered asynchronously.
func TestSlashHandlerDispatches(t *testing.T) {
	t.Parallel()
	ch := make(chan string, 1)
	h := NewSlashHandler(okEngine(), chanResponder{ch: ch}, testSecret, WithSlashClock(fixedNow))
	body := "command=%2Fwhotoask&text=billing&user_id=U2&response_url=" +
		"https%3A%2F%2Fhooks.slack.com%2FT123"

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signReq(t, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	select {
	case txt := <-ch:
		if !strings.Contains(txt, "Jane Roe") {
			t.Errorf("answer missing content: %s", txt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no slash answer dispatched")
	}
}

// TestSlashHandlerBadSignature verifies a wrong signature is rejected.
func TestSlashHandlerBadSignature(t *testing.T) {
	t.Parallel()
	h := NewSlashHandler(okEngine(), chanResponder{ch: make(chan string, 1)}, testSecret,
		WithSlashClock(fixedNow))
	req := signReq(t, "text=billing")
	req.Header.Set("X-Slack-Signature", "v0=deadbeef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, want 401", rec.Code)
	}
}

// TestSocketSlashEnvelope verifies a slash_commands frame is acked and
// answered through the responder.
func TestSocketSlashEnvelope(t *testing.T) {
	t.Parallel()
	ch := make(chan string, 1)
	s := NewSocketRunner(stubApp(t), okEngine(), &recorder{}, "UBOT",
		WithResponder(chanResponder{ch: ch}))
	conn := &fakeConn{reads: [][]byte{
		[]byte(`{"type":"slash_commands","envelope_id":"s1","payload":{` +
			`"command":"/whotoask","text":"billing","user_id":"U2",` +
			`"response_url":"https://hooks.slack.com/T"}}`),
	}}

	_ = s.session(context.Background(), conn)
	select {
	case txt := <-ch:
		if !strings.Contains(txt, "Jane Roe") {
			t.Errorf("answer missing content: %s", txt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no slash answer dispatched")
	}
	if len(conn.writes) != 1 || !strings.Contains(string(conn.writes[0]), "s1") {
		t.Errorf("acks = %v, want envelope s1 acknowledged", conn.writes)
	}
}
