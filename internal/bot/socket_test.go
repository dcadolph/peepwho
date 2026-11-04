package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dcadolph/peepwho/internal/resolve"
	"github.com/dcadolph/peepwho/internal/slack"
)

// fakeConn yields canned frames then EOF, recording writes.
type fakeConn struct {
	reads  [][]byte
	idx    int
	writes [][]byte
}

// Read returns the next canned frame or io.EOF.
func (f *fakeConn) Read(context.Context) ([]byte, error) {
	if f.idx >= len(f.reads) {
		return nil, io.EOF
	}
	b := f.reads[f.idx]
	f.idx++
	return b, nil
}

// Write records the sent frame.
func (f *fakeConn) Write(_ context.Context, data []byte) error {
	f.writes = append(f.writes, append([]byte(nil), data...))
	return nil
}

// Close is a no-op.
func (f *fakeConn) Close() error { return nil }

// Ping answers immediately: these tests exercise frame handling, not the
// keepalive, and a healthy connection is what they assume.
func (f *fakeConn) Ping(context.Context) error { return nil }

// stubApp returns a client with a dummy app token; session does not call it.
func stubApp(t *testing.T) *slack.Client {
	t.Helper()
	return slack.New("xapp-test")
}

// okEngine returns an engine whose ask always answers.
func okEngine() *Engine {
	return New(func(context.Context, string, string, int) (resolve.Answer, error) {
		return sampleAnswer(), nil
	}, "keyword", "UBOT", 5)
}

// TestSocketSessionDispatchesAndAcks verifies a mention is answered and acked.
func TestSocketSessionDispatchesAndAcks(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	s := NewSocketRunner(stubApp(t), okEngine(), rec, "UBOT")
	conn := &fakeConn{reads: [][]byte{
		[]byte(`{"type":"hello"}`),
		[]byte(`{"type":"events_api","envelope_id":"e1","payload":{"event":{` +
			`"type":"app_mention","text":"<@UBOT> billing","channel":"C1","user":"U2","ts":"5.5"}}}`),
	}}

	_ = s.session(context.Background(), conn)

	if rec.calls != 1 || rec.channel != "C1" {
		t.Fatalf("expected one reply to C1, got %+v", rec)
	}
	if rec.thread != "5.5" {
		t.Errorf("mention reply should thread on ts, got %q", rec.thread)
	}
	acked := false
	for _, w := range conn.writes {
		var m map[string]string
		if json.Unmarshal(w, &m) == nil && m["envelope_id"] == "e1" {
			acked = true
		}
	}
	if !acked {
		t.Error("envelope e1 was not acked")
	}
}

// blockingConn feeds canned frames, then blocks Read until hold closes, so a
// session stays open while a test observes acks. Writes land on a channel so a
// test can watch them arrive concurrently.
type blockingConn struct {
	reads  [][]byte
	idx    int
	writes chan []byte
	hold   chan struct{}
}

// Read returns the next canned frame, then waits for hold before signaling EOF.
func (c *blockingConn) Read(ctx context.Context) ([]byte, error) {
	if c.idx < len(c.reads) {
		b := c.reads[c.idx]
		c.idx++
		return b, nil
	}
	select {
	case <-c.hold:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Write publishes the frame to the writes channel.
func (c *blockingConn) Write(_ context.Context, data []byte) error {
	c.writes <- append([]byte(nil), data...)
	return nil
}

// Close is a no-op.
func (c *blockingConn) Close() error { return nil }

// Ping answers immediately, so a blocked read is what the test observes rather
// than the keepalive closing the connection underneath it.
func (c *blockingConn) Ping(context.Context) error { return nil }

// safeRecorder counts replies under a mutex, for tests that answer from more
// than one goroutine at once.
type safeRecorder struct {
	mu    sync.Mutex
	calls int
}

// Reply records one reply.
func (r *safeRecorder) Reply(_ context.Context, _, _, _ string) error {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return nil
}

// mentionFrame builds an app_mention events_api envelope with the given id.
func mentionFrame(id, text string) []byte {
	return []byte(fmt.Sprintf(
		`{"type":"events_api","envelope_id":%q,"payload":{"event":{`+
			`"type":"app_mention","text":%q,"channel":"C1","user":"U2","ts":"5.5"}}}`, id, text))
}

// TestSocketReadLoopNotBlockedBySlowAnswer verifies a slow answer does not stall
// the read loop: the second envelope must be acked while the first answer is
// still in flight. An inline handler would never reach the second Read.
func TestSocketReadLoopNotBlockedBySlowAnswer(t *testing.T) {
	t.Parallel()
	var once sync.Once
	started := make(chan struct{})
	release := make(chan struct{})
	eng := New(func(ctx context.Context, _, _ string, _ int) (resolve.Answer, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-ctx.Done():
			return resolve.Answer{}, ctx.Err()
		}
		return sampleAnswer(), nil
	}, "keyword", "UBOT", 5)

	s := NewSocketRunner(stubApp(t), eng, &safeRecorder{}, "UBOT")
	conn := &blockingConn{
		writes: make(chan []byte, 8),
		hold:   make(chan struct{}),
		reads:  [][]byte{mentionFrame("e1", "billing"), mentionFrame("e2", "kafka")},
	}

	done := make(chan struct{})
	go func() { _ = s.session(context.Background(), conn); close(done) }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first answer never started")
	}

	acked := map[string]bool{}
	for len(acked) < 2 {
		select {
		case w := <-conn.writes:
			var m map[string]string
			if json.Unmarshal(w, &m) == nil && m["envelope_id"] != "" {
				acked[m["envelope_id"]] = true
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("read loop blocked by slow answer: acked only %v", acked)
		}
	}
	if !acked["e1"] || !acked["e2"] {
		t.Errorf("acked = %v, want both e1 and e2", acked)
	}

	close(release)
	close(conn.hold)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not return after release")
	}
}

// TestSocketIgnoresOwnAndOther verifies self, bot, and non-mention messages are
// not answered.
func TestSocketIgnoresOwnAndOther(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	s := NewSocketRunner(stubApp(t), okEngine(), rec, "UBOT")
	conn := &fakeConn{reads: [][]byte{
		[]byte(`{"type":"events_api","envelope_id":"e1","payload":{"event":{` +
			`"type":"app_mention","text":"hi","channel":"C1","user":"UBOT","ts":"1"}}}`),
		[]byte(`{"type":"events_api","envelope_id":"e2","payload":{"event":{` +
			`"type":"message","text":"hi","channel":"C1","bot_id":"B1"}}}`),
		[]byte(`{"type":"events_api","envelope_id":"e3","payload":{"event":{` +
			`"type":"message","channel_type":"channel","text":"hi","channel":"C1","user":"U2"}}}`),
	}}

	_ = s.session(context.Background(), conn)

	if rec.calls != 0 {
		t.Errorf("should ignore self, bot, and non-mention messages, calls=%d", rec.calls)
	}
}

// deadConn never delivers a frame and never answers a ping. It is what a
// closed laptop lid or a NAT timeout leaves behind: the socket looks open, so
// a reader waits on it forever.
type deadConn struct {
	// closed is closed when the connection is shut, unblocking Read.
	closed chan struct{}
	// once guards Close against being called twice.
	once sync.Once
}

// newDeadConn returns a connection that answers nothing.
func newDeadConn() *deadConn { return &deadConn{closed: make(chan struct{})} }

// Read blocks until the connection is closed.
func (d *deadConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-d.closed:
		return nil, errors.New("connection closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Write reports success without sending anything.
func (d *deadConn) Write(context.Context, []byte) error { return nil }

// Ping never answers, which is what tells a half-open link from a quiet one.
func (d *deadConn) Ping(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// Close unblocks Read.
func (d *deadConn) Close() error {
	d.once.Do(func() { close(d.closed) })
	return nil
}

// TestSessionEndsWhenPingsGoUnanswered verifies a connection that stops
// answering is given up on rather than waited on forever. Slack sends nothing
// to a quiet workspace, so silence cannot be the signal; an unanswered ping
// can. Without this the bot would sit on a dead socket and never reconnect.
func TestSessionEndsWhenPingsGoUnanswered(t *testing.T) {
	t.Parallel()
	s := NewSocketRunner(stubApp(t), okEngine(), &recorder{}, "UBOT")
	// Probe far faster than production so the test does not wait on a timer.
	s.pingPeriod, s.pingTimeout = 10*time.Millisecond, 20*time.Millisecond
	done := make(chan error, 1)
	go func() { done <- s.session(context.Background(), newDeadConn()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("session returned no error for a connection that stopped answering")
			return
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session waited on a dead connection instead of giving up")
	}
}

// TestSocketHandlesBurstUnderLoad fires a burst of mentions through one session
// and verifies every one is answered and acked, the concurrent-answer cap is
// respected, and nothing races. The socket path spawns a goroutine per answer
// behind a slot pool, so a burst is where a concurrency bug would surface.
func TestSocketHandlesBurstUnderLoad(t *testing.T) {
	t.Parallel()
	const events = 200
	var inflight, maxInflight int64
	eng := New(func(context.Context, string, string, int) (resolve.Answer, error) {
		n := atomic.AddInt64(&inflight, 1)
		for {
			m := atomic.LoadInt64(&maxInflight)
			if n <= m || atomic.CompareAndSwapInt64(&maxInflight, m, n) {
				break
			}
		}
		runtime.Gosched()
		atomic.AddInt64(&inflight, -1)
		return sampleAnswer(), nil
	}, "keyword", "UBOT", 5)

	rec := &safeRecorder{}
	s := NewSocketRunner(stubApp(t), eng, rec, "UBOT")
	frames := [][]byte{[]byte(`{"type":"hello"}`)}
	for i := range events {
		frames = append(frames, []byte(fmt.Sprintf(
			`{"type":"events_api","envelope_id":"e%d","payload":{"event":{`+
				`"type":"app_mention","text":"<@UBOT> billing retries",`+
				`"channel":"C1","user":"U%d","ts":"%d.5"}}}`, i, i, i)))
	}
	conn := &fakeConn{reads: frames}

	_ = s.session(context.Background(), conn)

	if rec.calls != events {
		t.Errorf("answered %d of %d events", rec.calls, events)
	}
	if maxInflight > maxConcurrentAnswers {
		t.Errorf("peak concurrent answers = %d, want the cap of %d", maxInflight, maxConcurrentAnswers)
	}
	acked := 0
	for _, w := range conn.writes {
		var m map[string]string
		if json.Unmarshal(w, &m) == nil && strings.HasPrefix(m["envelope_id"], "e") {
			acked++
		}
	}
	if acked != events {
		t.Errorf("acked %d of %d envelopes", acked, events)
	}
}
