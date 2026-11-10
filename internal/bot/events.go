package bot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// maxBody bounds the request body the events handler reads.
const maxBody = 1 << 20

// EventsHandler serves the Slack Events API. It verifies the request signature,
// answers the url_verification handshake, and dispatches event callbacks.
type EventsHandler struct {
	// engine answers questions.
	engine *Engine
	// replier posts answers back to Slack.
	replier Replier
	// botUserID is the bot's own user id, used to ignore its own messages.
	botUserID string
	// signingSecret verifies request signatures.
	signingSecret string
	// maxSkew is the allowed clock skew for the request timestamp.
	maxSkew time.Duration
	// now returns the current time; overridable for tests.
	now func() time.Time
	// log receives handler notices.
	log io.Writer
	// slots bounds concurrent answers. Slack retries an event it does not see
	// acknowledged, and each answer can run a model call, so without a bound a
	// burst of mentions would spawn unbounded work on the machine serving it.
	slots chan struct{}
}

// EventsOption configures an EventsHandler.
type EventsOption func(*EventsHandler)

// WithClock overrides the clock, for tests.
func WithClock(now func() time.Time) EventsOption {
	return func(h *EventsHandler) {
		if now != nil {
			h.now = now
		}
	}
}

// WithEventsLog sets where handler notices are written.
func WithEventsLog(w io.Writer) EventsOption {
	return func(h *EventsHandler) {
		if w != nil {
			h.log = w
		}
	}
}

// NewEventsHandler builds an EventsHandler. It panics on a nil engine or
// replier, or an empty signing secret.
func NewEventsHandler(engine *Engine, replier Replier, botUserID, signingSecret string, opts ...EventsOption) *EventsHandler {
	if engine == nil || replier == nil {
		panic("bot: NewEventsHandler requires an engine and a replier")
	}
	if signingSecret == "" {
		panic("bot: NewEventsHandler requires a signing secret")
	}
	h := &EventsHandler{
		engine: engine, replier: replier, botUserID: botUserID,
		signingSecret: signingSecret, maxSkew: 5 * time.Minute,
		now: time.Now, log: io.Discard,
		slots: make(chan struct{}, maxConcurrentAnswers),
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// ServeHTTP verifies the request, answers url_verification, and dispatches
// event callbacks asynchronously so Slack receives a fast acknowledgment.
func (h *EventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	ts := r.Header.Get("X-Slack-Request-Timestamp")
	if !freshTimestamp(h.now, h.maxSkew, ts) {
		http.Error(w, "stale or missing timestamp", http.StatusBadRequest)
		return
	}
	if !verifySignature(h.signingSecret, r.Header.Get("X-Slack-Signature"), ts, body) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}

	var outer struct {
		// Type is the payload type.
		Type string `json:"type"`
		// Challenge is echoed for url_verification.
		Challenge string `json:"challenge"`
		// Event is the Slack event for event_callback.
		Event slackEvent `json:"event"`
	}
	if err := json.Unmarshal(body, &outer); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	switch outer.Type {
	case "url_verification":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, outer.Challenge)
	case "event_callback":
		w.WriteHeader(http.StatusOK)
		// A retry means the first delivery was already handled or is still
		// in flight; processing it again would double-reply.
		if retry := r.Header.Get("X-Slack-Retry-Num"); retry != "" && retry != "0" {
			fmt.Fprintf(h.log, "whotoask bot: skipping retry %s of an event\n", retry)
			return
		}
		ev := outer.Event
		select {
		case h.slots <- struct{}{}:
		default:
			// Every slot is busy. Dropping the event is better than queueing
			// work that outlives the question, and Slack has already been
			// acknowledged so it will not retry.
			fmt.Fprintln(h.log, "whotoask bot: busy, dropped an event")
			return
		}
		go func() { //nolint:contextcheck // Reply outlives the request.
			defer func() { <-h.slots }()
			routeEvent(context.Background(), h.engine, h.replier, h.botUserID, ev, h.log)
		}()
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// freshTimestamp reports whether ts is a unix time within skew of now. Both
// Slack HTTP handlers share it.
func freshTimestamp(now func() time.Time, skew time.Duration, ts string) bool {
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	delta := now().Unix() - n
	if delta < 0 {
		delta = -delta
	}
	return time.Duration(delta)*time.Second <= skew
}

// verifySignature checks the Slack request signature over the raw body. Both
// Slack HTTP handlers share it.
func verifySignature(secret, sig, ts string, body []byte) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = io.WriteString(mac, "v0:"+ts+":")
	mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

// compile-time guard that EventsHandler is an http.Handler.
var _ http.Handler = (*EventsHandler)(nil)
