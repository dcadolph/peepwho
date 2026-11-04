package bot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/peepwho/internal/recall"
	"github.com/dcadolph/peepwho/internal/resolve"
)

// publicOnlyReplier records channel replies and cannot reply privately.
type publicOnlyReplier struct {
	// text is the last reply posted to the channel.
	text string
}

// Reply records a channel reply.
func (r *publicOnlyReplier) Reply(_ context.Context, _, _, text string) error {
	r.text = text
	return nil
}

// privateReplier records both channel and private replies.
type privateReplier struct {
	// public is the last reply posted to the channel.
	public string
	// private is the last reply posted to one user.
	private string
	// user is who the private reply went to.
	user string
}

// Reply records a channel reply.
func (r *privateReplier) Reply(_ context.Context, _, _, text string) error {
	r.public = text
	return nil
}

// ReplyPrivately records a reply meant for one person.
func (r *privateReplier) ReplyPrivately(_ context.Context, _, user, text string) error {
	r.user, r.private = user, text
	return nil
}

// testAnswer is the recall answer the stub returns.
var testAnswer = recall.Answer{
	Query: "certificate renewal",
	Episodes: []recall.Episode{{
		People:    []recall.Person{{Name: "Billy Ray", Email: "billy@x.com"}},
		Place:     "infra",
		Source:    "slack",
		Kind:      "thread",
		When:      time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC),
		Permalink: "https://acme.slack.com/archives/C1/p1",
		Strength:  0.8,
	}},
	Scope: recall.Scope{Sources: []string{"slack"}, Episodes: 1, Note: "Covers indexed channels."},
}

// stubEngine returns an engine whose recall always answers with testAnswer.
func stubEngine() *Engine {
	ask := func(context.Context, string, string, int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	return New(ask, "keyword", "UBOT", 5, WithRecall(
		func(context.Context, string, string, int) (recall.Answer, error) {
			return testAnswer, nil
		}))
}

// TestRecallNeverPostsInChannel verifies a recall answer only ever goes to the
// asker. A transport that cannot deliver privately gets a refusal instead,
// because the answer describes one person's own conversations.
func TestRecallNeverPostsInChannel(t *testing.T) {
	t.Parallel()
	ev := Event{Text: "<@UBOT> recall certificate renewal", Channel: "C1", User: "U1"}

	public := &publicOnlyReplier{}
	if err := stubEngine().Handle(context.Background(), ev, public); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if public.text != noPrivateReply {
		t.Errorf("public reply = %q, want the refusal", public.text)
	}
	if strings.Contains(public.text, "Billy") || strings.Contains(public.text, "infra") {
		t.Error("a recall answer leaked into the channel")
	}

	private := &privateReplier{}
	if err := stubEngine().Handle(context.Background(), ev, private); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if private.public != "" {
		t.Errorf("channel reply = %q, want nothing posted in channel", private.public)
	}
	if private.user != "U1" {
		t.Errorf("private reply went to %q, want U1", private.user)
	}
	if !strings.Contains(private.private, "Billy Ray") {
		t.Errorf("private reply = %q, want it to name the person who helped", private.private)
	}
}

// TestParseRecall verifies which messages ask for recall and which are
// ordinary questions.
func TestParseRecall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In        string
		WantQuery string
		WantOK    bool
	}{
		{In: "recall certificate renewal", WantQuery: "certificate renewal", WantOK: true},
		{In: "  RECALL  kafka lag  ", WantQuery: "kafka lag", WantOK: true},
		{In: "recall: kafka lag", WantQuery: "kafka lag", WantOK: true},
		{In: "recall", WantQuery: "", WantOK: true},
		{In: "recalls of defective parts", WantOK: false},
		{In: "who knows about recall", WantOK: false},
		{In: "", WantOK: false},
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			query, ok := parseRecall(test.In)
			if ok != test.WantOK {
				t.Fatalf("parseRecall(%q) ok = %v, want %v", test.In, ok, test.WantOK)
			}
			if ok && query != test.WantQuery {
				t.Errorf("parseRecall(%q) = %q, want %q", test.In, query, test.WantQuery)
			}
		})
	}
}

// TestFormatRecall verifies the rendered answer names people, places, dates,
// and a working link, and that an empty answer explains its scope instead of
// implying nothing ever happened.
func TestFormatRecall(t *testing.T) {
	t.Parallel()
	got := FormatRecall("certificate renewal", testAnswer)
	for _, want := range []string{
		"Billy Ray", "#infra", "March 12, 2026",
		"<https://acme.slack.com/archives/C1/p1|open the conversation>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted answer missing %q:\n%s", want, got)
		}
	}

	empty := FormatRecall("nothing", recall.Answer{
		Scope: recall.Scope{Note: "Covers indexed channels."},
	})
	if !strings.Contains(empty, "Covers indexed channels.") {
		t.Errorf("empty answer = %q, want it to state what was searched", empty)
	}
}

// TestFormatRecallEscapes verifies values from the graph cannot inject Slack
// markup, and that a link peepwho did not build is dropped rather than rendered.
func TestFormatRecallEscapes(t *testing.T) {
	t.Parallel()
	got := FormatRecall("q", recall.Answer{
		Episodes: []recall.Episode{{
			People:    []recall.Person{{Name: "<!channel>"}},
			Place:     "a<b>c",
			Permalink: "javascript:alert(1)",
		}},
	})
	if strings.Contains(got, "<!channel>") || strings.Contains(got, "a<b>c") {
		t.Errorf("markup survived escaping:\n%s", got)
	}
	if strings.Contains(got, "javascript:") {
		t.Errorf("a non-https link was rendered:\n%s", got)
	}
}

// TestRecallDisabled verifies a bot built without recall says so rather than
// failing or answering from someone else's history.
func TestRecallDisabled(t *testing.T) {
	t.Parallel()
	ask := func(context.Context, string, string, int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	e := New(ask, "keyword", "UBOT", 5)
	got, err := e.Recall(context.Background(), "U1", "anything", 3)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if diff := cmp.Diff("Recall is not enabled on this bot.", got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}
