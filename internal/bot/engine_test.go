package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dcadolph/whotoask/internal/resolve"
)

// okAsk returns a fixed answer and records the query and mode it was called
// with, for asserting how the engine parses input.
func okAsk(query, mode *string) AskFunc {
	return func(_ context.Context, q, m string, _ int) (resolve.Answer, error) {
		if query != nil {
			*query = q
		}
		if mode != nil {
			*mode = m
		}
		return resolve.Answer{Summary: "ok"}, nil
	}
}

// TestEngineAnswerParsing verifies the bot mention and mode hint are stripped
// and an empty question never reaches the resolver.
func TestEngineAnswerParsing(t *testing.T) {
	t.Parallel()
	var gotQuery, gotMode string
	e := New(okAsk(&gotQuery, &gotMode), "keyword", "B1", 5)

	if _, err := e.Answer(context.Background(), "u", "<@B1> who owns billing --llm"); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if gotQuery != "who owns billing" || gotMode != "llm" {
		t.Errorf("query=%q mode=%q, want cleaned query and llm mode", gotQuery, gotMode)
	}

	gotQuery = "sentinel"
	if reply, err := e.Answer(context.Background(), "u", "<@B1>"); reply != "" || err != nil {
		t.Errorf("empty question: reply=%q err=%v, want empty reply and no error", reply, err)
	}
	if gotQuery != "sentinel" {
		t.Error("empty question should not reach the resolver")
	}
}

// TestEngineRateLimit verifies a user is allowed rateMax asks per window, warned
// once when over, silenced after, and allowed again in a fresh window.
func TestEngineRateLimit(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var asks int
	ask := func(_ context.Context, _, _ string, _ int) (resolve.Answer, error) {
		asks++
		return resolve.Answer{Summary: "ok"}, nil
	}
	e := New(ask, "keyword", "", 5, WithEngineClock(func() time.Time { return now }))

	for i := range rateMax {
		reply, err := e.Answer(context.Background(), "u1", "who owns billing")
		if err != nil || reply == "" {
			t.Fatalf("ask %d: reply=%q err=%v, want an answer", i, reply, err)
		}
	}
	if reply, _ := e.Answer(context.Background(), "u1", "again"); reply != rateWarning {
		t.Errorf("over-limit reply = %q, want the rate warning", reply)
	}
	if reply, _ := e.Answer(context.Background(), "u1", "again"); reply != "" {
		t.Errorf("second over-limit reply = %q, want silence", reply)
	}
	if asks != rateMax {
		t.Errorf("resolver calls = %d, want %d; over-limit asks must not reach it", asks, rateMax)
	}

	now = now.Add(rateWindow)
	if reply, err := e.Answer(context.Background(), "u1", "who owns billing"); err != nil || reply == "" {
		t.Errorf("after the window: reply=%q err=%v, want an answer again", reply, err)
	}
}

// TestEngineAnswerResolveError verifies a resolve failure yields a generic
// apology to the user while the real error is returned for the transport log.
func TestEngineAnswerResolveError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("resolver down")
	ask := func(_ context.Context, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, wantErr
	}
	e := New(ask, "keyword", "", 5)

	reply, err := e.Answer(context.Background(), "u", "who owns billing")
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want the resolve error surfaced for the log", err)
	}
	if reply != sorryReply {
		t.Errorf("reply = %q, want the generic apology", reply)
	}
}
