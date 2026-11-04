package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/dcadolph/peepwho/internal/slack"
)

// wsConn is the minimal WebSocket the socket runner needs.
type wsConn interface {
	// Read returns the next text message.
	Read(ctx context.Context) ([]byte, error)
	// Write sends a text message.
	Write(ctx context.Context, data []byte) error
	// Ping sends a WebSocket ping and waits for the matching pong. It is how a
	// connection that stopped carrying traffic is told apart from a quiet one.
	Ping(ctx context.Context) error
	// Close closes the connection.
	Close() error
}

// Dialer opens a WebSocket to url.
type Dialer func(ctx context.Context, url string) (wsConn, error)

// socketEnvelope is a Socket Mode frame.
type socketEnvelope struct {
	// Type is the frame type: hello, disconnect, events_api, or
	// slash_commands.
	Type string `json:"type"`
	// EnvelopeID identifies the frame for acknowledgment.
	EnvelopeID string `json:"envelope_id"`
	// Payload carries the event or slash command.
	Payload struct {
		// EventID identifies the event itself, unchanged across redeliveries,
		// which is what makes it usable for telling a repeat from a new event.
		EventID string `json:"event_id"`
		// Event is the Slack event, set on events_api frames.
		Event slackEvent `json:"event"`
		// Slash-command fields, set on slash_commands frames.
		Command     string `json:"command"`
		Text        string `json:"text"`
		UserID      string `json:"user_id"`
		ResponseURL string `json:"response_url"`
	} `json:"payload"`
}

// slackEvent is the subset of a Slack event the bot reads.
type slackEvent struct {
	// Type is the event type, such as app_mention or message.
	Type string `json:"type"`
	// Text is the message text.
	Text string `json:"text"`
	// Channel is the channel or DM id.
	Channel string `json:"channel"`
	// User is the author's user id.
	User string `json:"user"`
	// TS is the message timestamp.
	TS string `json:"ts"`
	// ThreadTS is the thread timestamp, if any.
	ThreadTS string `json:"thread_ts"`
	// BotID is set when a bot authored the message.
	BotID string `json:"bot_id"`
	// ChannelType is "im" for direct messages.
	ChannelType string `json:"channel_type"`
}

// Reconnect backoff bounds: failures back off exponentially from
// initialBackoff to maxBackoff, and a session that stayed healthy for
// steadyPeriod resets the backoff.
const (
	initialBackoff = time.Second
	maxBackoff     = 30 * time.Second
	steadyPeriod   = 30 * time.Second
)

// maxConcurrentAnswers caps how many answers run at once, so a burst of
// mentions cannot spawn unbounded resolver work.
const maxConcurrentAnswers = 8

// Keepalive bounds. A workspace can be quiet for hours, so silence alone says
// nothing about the connection. A ping that never draws a pong does: without
// it a half-open connection, which is what a closed laptop lid or a NAT
// timeout leaves behind, would keep the reader blocked forever and the bot
// would stop answering without ever reconnecting.
const (
	// defaultPingPeriod is how often the connection is probed.
	defaultPingPeriod = 30 * time.Second
	// defaultPingTimeout is how long a pong may take before the connection is
	// treated as dead.
	defaultPingTimeout = 10 * time.Second
)

// SocketRunner runs a Slack Socket Mode session: it opens a WebSocket with the
// app-level token, reads event frames, acknowledges them, and dispatches
// questions to the Engine. It reconnects with backoff until the context is
// canceled.
type SocketRunner struct {
	// app is the app-level token client used to open connections.
	app *slack.Client
	// engine answers questions.
	engine *Engine
	// replier posts answers back to Slack.
	replier Replier
	// respond posts slash-command answers; nil ignores slash frames.
	respond Responder
	// botUserID is the bot's own user id, used to ignore its own messages.
	botUserID string
	// dial opens the WebSocket; overridable for tests.
	dial Dialer
	// log receives connection notices.
	log io.Writer
	// answerSlots bounds concurrent answers to maxConcurrentAnswers. Each
	// answer goroutine acquires a slot itself so the read loop never blocks.
	answerSlots chan struct{}
	// pingPeriod is how often a session probes the connection, and pingTimeout
	// how long a pong may take. They are fields rather than constants so a
	// test can probe quickly without mutating state another test is reading.
	pingPeriod  time.Duration
	pingTimeout time.Duration
}

// SocketOption configures a SocketRunner.
type SocketOption func(*SocketRunner)

// WithDialer overrides the WebSocket dialer, for tests.
func WithDialer(d Dialer) SocketOption {
	return func(s *SocketRunner) {
		if d != nil {
			s.dial = d
		}
	}
}

// WithLog sets where connection notices are written.
func WithLog(w io.Writer) SocketOption {
	return func(s *SocketRunner) {
		if w != nil {
			s.log = w
		}
	}
}

// WithResponder enables slash-command answers through r.
func WithResponder(r Responder) SocketOption {
	return func(s *SocketRunner) {
		if r != nil {
			s.respond = r
		}
	}
}

// NewSocketRunner builds a SocketRunner. It panics on nil dependencies.
func NewSocketRunner(app *slack.Client, engine *Engine, replier Replier, botUserID string, opts ...SocketOption) *SocketRunner {
	if app == nil || engine == nil || replier == nil {
		panic("bot: NewSocketRunner requires app, engine, and replier")
	}
	s := &SocketRunner{
		app: app, engine: engine, replier: replier, botUserID: botUserID,
		dial: dialWebSocket, log: io.Discard,
		answerSlots: make(chan struct{}, maxConcurrentAnswers),
		pingPeriod:  defaultPingPeriod, pingTimeout: defaultPingTimeout,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Run opens connections and processes events until ctx is canceled. Failures
// anywhere, including the very first connection, reconnect with exponential
// backoff instead of exiting, so a laptop waking from sleep or a flapping
// network self-heals.
func (s *SocketRunner) Run(ctx context.Context) error {
	backoff := initialBackoff
	for {
		if ctx.Err() != nil {
			return nil
		}
		start := time.Now()
		err := s.connectAndServe(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) >= steadyPeriod {
			backoff = initialBackoff
		}
		fmt.Fprintf(s.log, "peepwho bot: reconnecting in %s after: %v\n", backoff, err)
		if !sleepCtx(ctx, backoff) {
			return nil
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// connectAndServe opens one Socket Mode session and serves it to completion.
func (s *SocketRunner) connectAndServe(ctx context.Context) error {
	url, err := s.app.ConnectionsOpen(ctx)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	conn, err := s.dial(ctx, url)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	return s.session(ctx, conn)
}

// sleepCtx waits for d, returning false when ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// session reads and dispatches frames until the connection ends. Answers run on
// their own goroutines so a slow one, up to handleTimeout, cannot stall the read
// loop from acking and reading the next envelope. Outstanding answers are
// drained before the session returns.
func (s *SocketRunner) session(ctx context.Context, conn wsConn) error {
	defer func() { _ = conn.Close() }()
	var answers sync.WaitGroup
	// Only the read loop below touches this, so it needs no lock.
	var handled recentEvents
	defer answers.Wait()

	// Closing the connection is what unblocks the read below, so the keepalive
	// reports a dead link by closing rather than by returning an error nobody
	// is waiting on.
	alive, stopKeepalive := context.WithCancel(ctx)
	defer stopKeepalive()
	go func() {
		ticker := time.NewTicker(s.pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-alive.Done():
				return
			case <-ticker.C:
				probe, cancel := context.WithTimeout(alive, s.pingTimeout)
				err := conn.Ping(probe)
				cancel()
				if err != nil && alive.Err() == nil {
					fmt.Fprintf(s.log, "peepwho bot: connection stopped answering: %v\n", err)
					_ = conn.Close()
					return
				}
			}
		}
	}()

	for {
		data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var env socketEnvelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch env.Type {
		case "hello":
			continue
		case "disconnect":
			return errors.New("server requested disconnect")
		default:
			if env.EnvelopeID != "" {
				s.ack(ctx, conn, env.EnvelopeID)
			}
			switch {
			case env.Type == "events_api":
				// Slack redelivers an event when an acknowledgment is slow or
				// lost, and answering it twice means the bot replies twice to
				// one question. The event's own id is stable across those
				// redeliveries, so a repeat is still acknowledged and then
				// dropped.
				if !handled.first(env.Payload.EventID) {
					continue
				}
				ev := env.Payload.Event
				s.dispatch(ctx, &answers, func() {
					routeEvent(ctx, s.engine, s.replier, s.botUserID, ev, s.log)
				})
			case env.Type == "slash_commands" && s.respond != nil:
				cmd := slashCommand{
					Text:        env.Payload.Text,
					UserID:      env.Payload.UserID,
					ResponseURL: env.Payload.ResponseURL,
				}
				s.dispatch(ctx, &answers, func() {
					routeSlash(ctx, s.engine, s.respond, cmd, s.log)
				})
			}
		}
	}
}

// recentEvents remembers the events already answered, so a redelivery is not
// answered again. It keeps a bounded window rather than everything: Slack only
// retries for a few minutes, and a session that runs for months must not grow a
// set for every message it ever saw.
type recentEvents struct {
	// seen holds the ids currently remembered.
	seen map[string]bool
	// order is those ids in arrival order, oldest first, for eviction.
	order []string
}

// maxRememberedEvents bounds how many event ids a session keeps. Slack gives up
// retrying long before this many events pass on any real workspace.
const maxRememberedEvents = 2048

// first reports whether this event has not been answered yet, and remembers it.
// An event with no id cannot be told apart from any other, so it is treated as
// new: answering twice is better than staying silent.
func (r *recentEvents) first(id string) bool {
	if id == "" {
		return true
	}
	if r.seen == nil {
		r.seen = make(map[string]bool, maxRememberedEvents)
	}
	if r.seen[id] {
		return false
	}
	r.seen[id] = true
	r.order = append(r.order, id)
	if len(r.order) > maxRememberedEvents {
		delete(r.seen, r.order[0])
		r.order = r.order[1:]
	}
	return true
}

// dispatch runs fn on its own goroutine after acquiring an answer slot, so the
// read loop never blocks: a slot fills only inside the goroutine. It abandons
// the work if ctx ends before a slot frees. The WaitGroup lets the session
// drain in-flight answers before it returns.
func (s *SocketRunner) dispatch(ctx context.Context, wg *sync.WaitGroup, fn func()) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case s.answerSlots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-s.answerSlots }()
		fn()
	}()
}

// ack acknowledges a frame so Slack does not redeliver it.
func (s *SocketRunner) ack(ctx context.Context, conn wsConn, id string) {
	body, err := json.Marshal(map[string]string{"envelope_id": id})
	if err != nil {
		return
	}
	_ = conn.Write(ctx, body)
}

// routeEvent answers a mention or direct message, ignoring the bot's own and
// other bots' messages. It is shared by the socket and events transports,
// recovers panics, and bounds the work so a hung resolver cannot pin the bot.
func routeEvent(ctx context.Context, e *Engine, r Replier, botUserID string, ev slackEvent, log io.Writer) {
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Fprintf(log, "peepwho bot: handler panic: %v\n", rec)
		}
	}()
	if ev.BotID != "" || (botUserID != "" && ev.User == botUserID) {
		return
	}
	mention := ev.Type == "app_mention"
	directMessage := ev.Type == "message" && ev.ChannelType == "im"
	if !mention && !directMessage {
		return
	}
	thread := ev.ThreadTS
	if thread == "" && mention {
		thread = ev.TS
	}
	ctx, cancel := context.WithTimeout(ctx, handleTimeout)
	defer cancel()
	event := Event{Text: ev.Text, Channel: ev.Channel, ThreadTS: thread, User: ev.User}
	if err := e.Handle(ctx, event, r); err != nil {
		fmt.Fprintf(log, "peepwho bot: handle: %v\n", err)
	}
}

// dialWebSocket is the production dialer backed by the WebSocket library.
func dialWebSocket(ctx context.Context, url string) (wsConn, error) {
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(1 << 20)
	return &coderConn{c: c}, nil
}

// coderConn adapts the WebSocket library connection to wsConn.
type coderConn struct {
	// c is the underlying connection.
	c *websocket.Conn
}

// Read returns the next text message.
func (cc *coderConn) Read(ctx context.Context) ([]byte, error) {
	_, data, err := cc.c.Read(ctx)
	return data, err
}

// Write sends a text message.
func (cc *coderConn) Write(ctx context.Context, data []byte) error {
	return cc.c.Write(ctx, websocket.MessageText, data)
}

// Ping sends a ping and waits for the pong.
func (cc *coderConn) Ping(ctx context.Context) error { return cc.c.Ping(ctx) }

// Close closes the connection normally.
func (cc *coderConn) Close() error {
	return cc.c.Close(websocket.StatusNormalClosure, "")
}
