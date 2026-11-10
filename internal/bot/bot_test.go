package bot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// recorder captures the last reply for assertions.
type recorder struct {
	channel, thread, text string
	calls                 int
}

// Reply records the reply.
func (r *recorder) Reply(_ context.Context, channel, threadTS, text string) error {
	r.channel, r.thread, r.text = channel, threadTS, text
	r.calls++
	return nil
}

// sampleAnswer returns an answer with one person and one channel.
func sampleAnswer() resolve.Answer {
	return resolve.Answer{
		Summary: "Talk to Jane.",
		People: []model.Match{{
			Person: &model.Person{Name: "Jane Roe", Email: "jane@x.com", Title: "Engineer"},
			Team:   &model.Team{Name: "Billing"},
		}},
		Channels: []model.ChannelMatch{{Channel: &model.Channel{Name: "billing", Topic: "retries"}}},
	}
}

// nilAsk is an AskFunc that returns an empty answer.
func nilAsk(context.Context, string, string, int) (resolve.Answer, error) {
	return resolve.Answer{}, nil
}

// TestParse covers mention stripping and mode hints.
func TestParse(t *testing.T) {
	t.Parallel()
	e := New(nilAsk, "keyword", "U1", 5)
	tests := []struct{ In, WantQuery, WantMode string }{
		{In: "<@U1> who owns billing --llm", WantQuery: "who owns billing", WantMode: "llm"},
		{In: "kafka", WantQuery: "kafka", WantMode: "keyword"},
		{In: "mode:keyword retries", WantQuery: "retries", WantMode: "keyword"},
	}
	for _, tt := range tests {
		q, m := e.parse(tt.In)
		if q != tt.WantQuery || m != tt.WantMode {
			t.Errorf("parse(%q) = (%q,%q), want (%q,%q)", tt.In, q, m, tt.WantQuery, tt.WantMode)
		}
	}
}

// TestHandleReplies verifies a question is resolved and answered in place.
func TestHandleReplies(t *testing.T) {
	t.Parallel()
	var gotMode string
	ask := func(_ context.Context, _, mode string, _ int) (resolve.Answer, error) {
		gotMode = mode
		return sampleAnswer(), nil
	}
	e := New(ask, "keyword", "U1", 5)
	rec := &recorder{}
	ev := Event{Text: "<@U1> billing --llm", Channel: "C1", ThreadTS: "9.9"}
	if err := e.Handle(context.Background(), ev, rec); err != nil {
		t.Fatal(err)
	}
	if rec.calls != 1 || rec.channel != "C1" || rec.thread != "9.9" {
		t.Errorf("reply target wrong: %+v", rec)
	}
	if gotMode != "llm" {
		t.Errorf("mode = %q, want llm", gotMode)
	}
	if !strings.Contains(rec.text, "Jane Roe") || !strings.Contains(rec.text, "#billing") {
		t.Errorf("reply missing content:\n%s", rec.text)
	}
}

// TestRateLimit verifies the per-user limit: the questions inside a window
// are answered, crossing the limit warns exactly once, further questions are
// dropped silently, and a new window resets everything.
func TestRateLimit(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	e := New(func(context.Context, string, string, int) (resolve.Answer, error) {
		return sampleAnswer(), nil
	}, "keyword", "UBOT", 5, WithEngineClock(func() time.Time { return now }))

	for i := 0; i < rateMax; i++ {
		if text, err := e.Answer(context.Background(), "U1", "billing"); err != nil || text == "" {
			t.Fatalf("ask %d: text=%q err=%v, want an answer", i, text, err)
		}
	}
	if text, _ := e.Answer(context.Background(), "U1", "billing"); text != rateWarning {
		t.Errorf("over-limit text = %q, want the warning", text)
	}
	if text, _ := e.Answer(context.Background(), "U1", "billing"); text != "" {
		t.Errorf("second over-limit text = %q, want silence", text)
	}
	if text, _ := e.Answer(context.Background(), "U2", "billing"); text == "" {
		t.Error("another user should be unaffected")
	}

	now = now.Add(rateWindow)
	if text, _ := e.Answer(context.Background(), "U1", "billing"); text == "" || text == rateWarning {
		t.Errorf("new window text = %q, want a fresh answer", text)
	}
}

// TestHandleErrorHidesInternals verifies a resolve failure sends the user a
// generic apology and returns the real error for the transport to log.
func TestHandleErrorHidesInternals(t *testing.T) {
	t.Parallel()
	askErr := errors.New("dial http://10.0.0.9:11434: connection refused")
	e := New(func(context.Context, string, string, int) (resolve.Answer, error) {
		return resolve.Answer{}, askErr
	}, "keyword", "U1", 5)
	rec := &recorder{}

	err := e.Handle(context.Background(), Event{Text: "<@U1> billing", Channel: "C1"}, rec)
	if !errors.Is(err, askErr) {
		t.Errorf("err = %v, want the resolve error for the transport log", err)
	}
	if rec.calls != 1 {
		t.Fatalf("calls = %d, want one apology reply", rec.calls)
	}
	if strings.Contains(rec.text, "10.0.0.9") || strings.Contains(rec.text, "connection refused") {
		t.Errorf("reply leaked internals:\n%s", rec.text)
	}
	if !strings.Contains(rec.text, "Sorry") {
		t.Errorf("reply = %q, want an apology", rec.text)
	}
}

// TestHandleEmptyIgnored verifies a mention with no question stays silent.
func TestHandleEmptyIgnored(t *testing.T) {
	t.Parallel()
	e := New(func(context.Context, string, string, int) (resolve.Answer, error) {
		return sampleAnswer(), nil
	}, "keyword", "U1", 5)
	rec := &recorder{}
	if err := e.Handle(context.Background(), Event{Text: "<@U1>", Channel: "C1"}, rec); err != nil {
		t.Fatal(err)
	}
	if rec.calls != 0 {
		t.Errorf("empty question should not reply, calls=%d", rec.calls)
	}
}

// TestFormatNoMatches verifies the no-result message.
func TestFormatNoMatches(t *testing.T) {
	t.Parallel()
	if got := Format("kafka", resolve.Answer{}); !strings.Contains(got, "No matches") {
		t.Errorf("got %q", got)
	}
}
