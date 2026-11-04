// Package bot answers peepwho questions from Slack. It is transport-agnostic: a
// socket-mode or events-api adapter normalizes Slack events into Events and
// feeds them to the Engine, which resolves and replies through a Replier.
package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/dcadolph/peepwho/internal/resolve"
)

// Per-user rate limiting: rateMax questions per rateWindow. The bot answers
// everyone in the workspace, so one looping user must not be able to burn
// model spend or drown a channel.
const (
	// rateMax is the questions one user may ask per window.
	rateMax = 10
	// rateWindow is the counting window.
	rateWindow = time.Minute
)

// rateWarning is sent once per window when a user goes over the limit.
const rateWarning = "You are asking faster than I can answer. Give it a minute and try again."

// sorryReply is sent when resolving fails; the real error goes to the
// transport log, never the channel.
const sorryReply = "Sorry, something went wrong answering that. Try again in a moment."

// Event is a normalized inbound message for the bot to answer.
type Event struct {
	// Text is the message text, possibly containing the bot mention.
	Text string
	// Channel is the channel or DM id to reply in.
	Channel string
	// ThreadTS is the thread timestamp to reply within, if any.
	ThreadTS string
	// User is the asking user's id, for per-user rate limiting.
	User string
}

// Replier sends a reply to Slack.
type Replier interface {
	// Reply posts text to channel, threading under threadTS when non-empty.
	Reply(ctx context.Context, channel, threadTS, text string) error
}

// AskFunc resolves a query in a mode, matching the CLI and web ask path.
type AskFunc func(ctx context.Context, query, mode string, limit int) (resolve.Answer, error)

// userWindow tracks one user's asks inside the current rate window.
type userWindow struct {
	// start opens the window.
	start time.Time
	// count is questions asked in the window.
	count int
	// warned marks that the over-limit notice was sent this window.
	warned bool
}

// Engine answers events by resolving the query and formatting a Slack reply.
type Engine struct {
	// ask resolves queries.
	ask AskFunc
	// defaultMode is used when the message carries no mode hint.
	defaultMode string
	// limit caps results per section.
	limit int
	// botUserID is the bot's own user id, stripped from inbound text.
	botUserID string
	// recall answers what the asker worked through before; nil disables it.
	recall RecallFunc
	// now returns the current time; tests pin it.
	now func() time.Time
	// mu guards windows.
	mu sync.Mutex
	// windows tracks per-user ask counts for rate limiting.
	windows map[string]*userWindow
}

// EngineOption configures an Engine.
type EngineOption func(*Engine)

// WithEngineClock overrides the clock, for tests.
func WithEngineClock(now func() time.Time) EngineOption {
	return func(e *Engine) {
		if now != nil {
			e.now = now
		}
	}
}

// New returns an Engine. It panics if ask is nil, and applies sensible defaults
// for an empty mode or non-positive limit.
func New(ask AskFunc, defaultMode, botUserID string, limit int, opts ...EngineOption) *Engine {
	if ask == nil {
		panic("bot: New requires an Ask function")
	}
	if defaultMode == "" {
		defaultMode = "keyword"
	}
	if limit <= 0 {
		limit = 5
	}
	e := &Engine{
		ask: ask, defaultMode: defaultMode, limit: limit, botUserID: botUserID,
		now: time.Now, windows: make(map[string]*userWindow),
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Answer resolves text asked by user and returns the reply. An empty reply
// means there is nothing to say: an empty question, or a rate-limited user
// already warned this window. A non-nil error reports a resolve failure the
// reply already apologizes for, so the transport can log it.
func (e *Engine) Answer(ctx context.Context, user, text string) (string, error) {
	query, mode := e.parse(text)
	if query == "" {
		return "", nil
	}
	allowed, warn := e.allow(user)
	if warn {
		return rateWarning, nil
	}
	if !allowed {
		return "", nil
	}
	ans, err := e.ask(ctx, query, mode, e.limit)
	if err != nil {
		return sorryReply, err
	}
	return Format(query, ans), nil
}

// Handle answers ev and replies through r. An empty question is ignored, a
// rate-limited user gets one warning per window, and a resolve error gets a
// generic apology while the error itself is returned for the transport log.
func (e *Engine) Handle(ctx context.Context, ev Event, r Replier) error {
	if query, ok := parseRecall(e.strip(ev.Text)); ok {
		return e.HandleRecall(ctx, ev, r, query)
	}
	text, err := e.Answer(ctx, ev.User, ev.Text)
	if text == "" {
		return err
	}
	replyErr := r.Reply(ctx, ev.Channel, ev.ThreadTS, text)
	return errors.Join(err, replyErr)
}

// allow reports whether user may ask now, and whether they just crossed the
// limit and deserve the one warning for this window. An empty user is never
// limited.
func (e *Engine) allow(user string) (allowed, warn bool) {
	if user == "" {
		return true, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	e.pruneWindows(now)
	w := e.windows[user]
	if w == nil || now.Sub(w.start) >= rateWindow {
		e.windows[user] = &userWindow{start: now, count: 1}
		return true, false
	}
	if w.count < rateMax {
		w.count++
		return true, false
	}
	if !w.warned {
		w.warned = true
		return false, true
	}
	return false, false
}

// pruneWindows drops expired windows once the map grows past a workspace's
// worth of users. Callers hold the lock.
func (e *Engine) pruneWindows(now time.Time) {
	if len(e.windows) < 512 {
		return
	}
	for user, w := range e.windows {
		if now.Sub(w.start) >= rateWindow {
			delete(e.windows, user)
		}
	}
}

// strip removes the bot's own mention from inbound text, so a message that
// addresses the bot reads the same as one that does not.
func (e *Engine) strip(text string) string {
	if e.botUserID == "" {
		return text
	}
	return strings.TrimSpace(strings.ReplaceAll(text, "<@"+e.botUserID+">", " "))
}

// parse strips the bot mention and a mode hint, returning the cleaned query and
// the chosen mode. A trailing or inline "--llm"/"--keyword" (or "mode:llm")
// selects the mode; otherwise the default applies.
func (e *Engine) parse(text string) (query, mode string) {
	mode = e.defaultMode
	fields := strings.Fields(e.strip(text))
	kept := fields[:0]
	for _, f := range fields {
		switch strings.ToLower(f) {
		case "--llm", "mode:llm":
			mode = "llm"
		case "--keyword", "mode:keyword":
			mode = "keyword"
		default:
			kept = append(kept, f)
		}
	}
	return strings.Join(kept, " "), mode
}
