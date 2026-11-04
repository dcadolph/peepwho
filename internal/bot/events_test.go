package bot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testSecret is the signing secret used by the events tests.
const testSecret = "shh"

// fixedNow pins the handler clock so timestamps are deterministic.
func fixedNow() time.Time { return time.Unix(1000, 0) }

// signReq builds a correctly signed request for body at timestamp 1000.
func signReq(t *testing.T, body string) *http.Request {
	t.Helper()
	const ts = "1000"
	mac := hmac.New(sha256.New, []byte(testSecret))
	_, _ = io.WriteString(mac, "v0:"+ts+":"+body)
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))
	req := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)
	return req
}

// newHandler builds an events handler with the test engine and clock.
func newHandler(replier Replier) *EventsHandler {
	return NewEventsHandler(okEngine(), replier, "UBOT", testSecret, WithClock(fixedNow))
}

// chanReplier signals replies on a channel.
type chanReplier struct {
	ch chan string
}

// Reply pushes the reply text onto the channel.
func (c chanReplier) Reply(_ context.Context, _, _, text string) error {
	c.ch <- text
	return nil
}

// TestEventsURLVerification verifies the handshake echoes the challenge.
func TestEventsURLVerification(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	newHandler(&recorder{}).ServeHTTP(rec, signReq(t, `{"type":"url_verification","challenge":"abc123"}`))
	if rec.Code != http.StatusOK || rec.Body.String() != "abc123" {
		t.Fatalf("code=%d body=%q, want 200 abc123", rec.Code, rec.Body.String())
	}
}

// TestEventsBadSignature verifies a wrong signature is rejected.
func TestEventsBadSignature(t *testing.T) {
	t.Parallel()
	req := signReq(t, `{"type":"url_verification","challenge":"x"}`)
	req.Header.Set("X-Slack-Signature", "v0=deadbeef")
	rec := httptest.NewRecorder()
	newHandler(&recorder{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, want 401", rec.Code)
	}
}

// TestEventsStaleTimestamp verifies an old timestamp is rejected.
func TestEventsStaleTimestamp(t *testing.T) {
	t.Parallel()
	body := `{"type":"url_verification","challenge":"x"}`
	mac := hmac.New(sha256.New, []byte(testSecret))
	_, _ = io.WriteString(mac, "v0:1:"+body)
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", "1")
	req.Header.Set("X-Slack-Signature", sig)

	rec := httptest.NewRecorder()
	newHandler(&recorder{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, want 400", rec.Code)
	}
}

// TestEventsRetrySkipped verifies a Slack redelivery is acknowledged but not
// answered again.
func TestEventsRetrySkipped(t *testing.T) {
	t.Parallel()
	ch := make(chan string, 1)
	h := newHandler(chanReplier{ch: ch})
	body := `{"type":"event_callback","event":{"type":"app_mention",` +
		`"text":"<@UBOT> billing","channel":"C1","user":"U2","ts":"5.5"}}`
	req := signReq(t, body)
	req.Header.Set("X-Slack-Retry-Num", "1")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	select {
	case txt := <-ch:
		t.Fatalf("retry was answered again: %s", txt)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestEventsCallbackDispatches verifies a signed event_callback is answered.
func TestEventsCallbackDispatches(t *testing.T) {
	t.Parallel()
	ch := make(chan string, 1)
	h := newHandler(chanReplier{ch: ch})
	body := `{"type":"event_callback","event":{"type":"app_mention",` +
		`"text":"<@UBOT> billing","channel":"C1","user":"U2","ts":"5.5"}}`

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signReq(t, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	select {
	case txt := <-ch:
		if !strings.Contains(txt, "Jane Roe") {
			t.Errorf("reply missing content: %s", txt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply dispatched")
	}
}
