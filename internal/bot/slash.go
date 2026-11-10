package bot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// handleTimeout bounds one question's resolve and reply, so a hung model
// cannot pin a handler goroutine forever.
const handleTimeout = 60 * time.Second

// slashUsage answers an empty /whotoask.
const slashUsage = "Ask like: /whotoask who owns billing retries"

// Responder posts a slash-command answer to its response URL.
type Responder interface {
	// Respond posts text to responseURL.
	Respond(ctx context.Context, responseURL, text string) error
}

// PrivateResponder posts a slash-command answer only the person who ran the
// command can see. A responder without it gets no recall answers.
type PrivateResponder interface {
	// RespondPrivately posts text to responseURL, visible only to the caller.
	RespondPrivately(ctx context.Context, responseURL, text string) error
}

// ResponderFunc adapts a function to the Responder interface.
type ResponderFunc func(ctx context.Context, responseURL, text string) error

// Respond calls f.
func (f ResponderFunc) Respond(ctx context.Context, responseURL, text string) error {
	return f(ctx, responseURL, text)
}

// slashCommand is one /whotoask invocation, from either transport.
type slashCommand struct {
	// Text is the question after the command word.
	Text string
	// UserID is the asking user, for rate limiting.
	UserID string
	// ResponseURL receives the answer.
	ResponseURL string
}

// routeSlash answers one slash command through its response URL. It recovers
// panics and bounds the work, so a bad question cannot take the bot down.
func routeSlash(ctx context.Context, e *Engine, respond Responder, cmd slashCommand, log io.Writer) {
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Fprintf(log, "whotoask bot: slash panic: %v\n", rec)
		}
	}()
	if cmd.ResponseURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, handleTimeout)
	defer cancel()

	if query, ok := parseRecall(cmd.Text); ok {
		routeSlashRecall(ctx, e, respond, cmd, query, log)
		return
	}

	text, err := e.Answer(ctx, cmd.UserID, cmd.Text)
	if err != nil {
		fmt.Fprintf(log, "whotoask bot: slash: %v\n", err)
	}
	if text == "" {
		text = slashUsage
	}
	if err := respond.Respond(ctx, cmd.ResponseURL, text); err != nil {
		fmt.Fprintf(log, "whotoask bot: slash respond: %v\n", err)
	}
}

// routeSlashRecall answers a recall request so only the person who ran the
// command sees it. A responder that cannot answer privately gets a refusal,
// since the answer describes that person's own conversations.
func routeSlashRecall(
	ctx context.Context, e *Engine, respond Responder, cmd slashCommand, query string, log io.Writer,
) {
	private, ok := respond.(PrivateResponder)
	if !ok {
		if err := respond.Respond(ctx, cmd.ResponseURL, noPrivateReply); err != nil {
			fmt.Fprintf(log, "whotoask bot: slash respond: %v\n", err)
		}
		return
	}
	allowed, warn := e.allow(cmd.UserID)
	if warn {
		if err := private.RespondPrivately(ctx, cmd.ResponseURL, rateWarning); err != nil {
			fmt.Fprintf(log, "whotoask bot: slash respond: %v\n", err)
		}
		return
	}
	if !allowed {
		return
	}
	text := "Tell me what to look for, like `/whotoask recall certificate renewal`."
	if query != "" {
		answer, err := e.Recall(ctx, cmd.UserID, query, recallLimit)
		if err != nil {
			fmt.Fprintf(log, "whotoask bot: slash recall: %v\n", err)
		}
		if answer != "" {
			text = answer
		}
	}
	if err := private.RespondPrivately(ctx, cmd.ResponseURL, text); err != nil {
		fmt.Fprintf(log, "whotoask bot: slash respond: %v\n", err)
	}
}

// SlashHandler serves /whotoask over HTTP for the Events API transport. It
// verifies the Slack signature, acknowledges immediately, and answers through
// the command's response URL.
type SlashHandler struct {
	// engine answers questions.
	engine *Engine
	// respond posts answers to response URLs.
	respond Responder
	// signingSecret verifies request signatures.
	signingSecret string
	// maxSkew is the allowed clock skew for the request timestamp.
	maxSkew time.Duration
	// now returns the current time; overridable for tests.
	now func() time.Time
	// log receives handler notices.
	log io.Writer
	// slots bounds concurrent answers, so a burst of commands cannot spawn
	// unbounded resolver work on the machine serving them.
	slots chan struct{}
}

// SlashOption configures a SlashHandler.
type SlashOption func(*SlashHandler)

// WithSlashClock overrides the clock, for tests.
func WithSlashClock(now func() time.Time) SlashOption {
	return func(h *SlashHandler) {
		if now != nil {
			h.now = now
		}
	}
}

// WithSlashLog sets where handler notices are written.
func WithSlashLog(w io.Writer) SlashOption {
	return func(h *SlashHandler) {
		if w != nil {
			h.log = w
		}
	}
}

// NewSlashHandler builds a SlashHandler. It panics on a nil engine or
// responder, or an empty signing secret.
func NewSlashHandler(engine *Engine, respond Responder, signingSecret string, opts ...SlashOption) *SlashHandler {
	if engine == nil || respond == nil {
		panic("bot: NewSlashHandler requires an engine and a responder")
	}
	if signingSecret == "" {
		panic("bot: NewSlashHandler requires a signing secret")
	}
	h := &SlashHandler{
		engine: engine, respond: respond, signingSecret: signingSecret,
		maxSkew: 5 * time.Minute, now: time.Now, log: io.Discard,
		slots: make(chan struct{}, maxConcurrentAnswers),
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// ServeHTTP verifies the request, acknowledges immediately so Slack's three
// second deadline is never missed, and answers asynchronously through the
// response URL.
func (h *SlashHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	vals, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
	cmd := slashCommand{
		Text:        vals.Get("text"),
		UserID:      vals.Get("user_id"),
		ResponseURL: vals.Get("response_url"),
	}
	select {
	case h.slots <- struct{}{}:
	default:
		fmt.Fprintln(h.log, "whotoask bot: busy, declined a slash command")
		_, _ = w.Write([]byte("whotoask is busy answering other questions. Try again in a moment."))
		return
	}
	go func() { //nolint:contextcheck // Reply outlives the request.
		defer func() { <-h.slots }()
		routeSlash(context.Background(), h.engine, h.respond, cmd, h.log)
	}()
}

// compile-time guard that SlashHandler is an http.Handler.
var _ http.Handler = (*SlashHandler)(nil)
