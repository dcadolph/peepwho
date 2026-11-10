package resolve

import (
	"context"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// captureChatter records what would leave the machine and replies with a
// fixed ranking by candidate number.
type captureChatter struct {
	// system is the captured system prompt.
	system string
	// user is the captured user prompt.
	user string
	// reply is returned to the resolver.
	reply string
}

// Chat records the prompts and returns the canned reply.
func (c *captureChatter) Chat(_ context.Context, system, user string) (string, error) {
	c.system, c.user = system, user
	return c.reply, nil
}

// redactIndex builds two people whose order the model will flip: Alice the
// clear topic owner retrieves first, Bob the passing mentioner second.
func redactIndex() *index.Index {
	ix := index.New()
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Email: "alice@corp.com", Name: "Alice Smith",
		Title: "Staff Engineer", Team: "Payments", Topics: []string{"billing"},
		Source: "org-csv",
	}, {
		Kind: connector.KindPerson, Email: "bob@corp.com", Name: "Bob Jones",
		Title: "Engineer", Team: "Payments", Text: "answered billing once in passing",
		Source: "org-csv",
	}, {
		Kind: connector.KindChannel, Name: "pay-help", Title: "billing questions",
		Members: []string{"alice@corp.com"}, Source: "slack",
	}})
	return ix
}

func TestRedactedLLMSendsNoIdentifiers(t *testing.T) {
	t.Parallel()
	chat := &captureChatter{reply: `{"people":["2","1"],"channels":["1"]}`}
	res := NewRedactedLLM(redactIndex(), chat, nil)

	ans, err := res.Resolve(context.Background(), "billing", 5)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	sent := strings.ToLower(chat.system + "\n" + chat.user)
	for _, leak := range []string{"alice", "bob", "@corp.com", "smith", "jones", "pay-help", "questions"} {
		if strings.Contains(sent, leak) {
			t.Errorf("prompt leaked %q:\n%s", leak, chat.user)
		}
	}
	if !strings.Contains(chat.user, "Staff Engineer") {
		t.Errorf("prompt missing role context:\n%s", chat.user)
	}

	if len(ans.People) != 2 || ans.People[0].Person.Email != "bob@corp.com" {
		t.Errorf("people order top = %v, want the model's number ranking applied", ans.People[0].Person.ID)
	}
	if len(ans.Channels) != 1 || ans.Channels[0].Channel.Name != "pay-help" {
		t.Errorf("channels = %+v", ans.Channels)
	}
	if !strings.Contains(ans.Summary, "Bob Jones") {
		t.Errorf("summary = %q, want a locally written name", ans.Summary)
	}
}

// TestRedactedLLMDecodesFlexibleRankings verifies the redacted re-ranking
// survives the shapes cloud models actually return: bare integer indices, a
// fenced code block, and prose preceding the JSON. Each must flip Alice and Bob
// the way the numbers 2 then 1 direct, not silently fall back to keyword order.
func TestRedactedLLMDecodesFlexibleRankings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name  string
		Reply string
	}{
		{Name: "bare integers", Reply: `{"people":[2,1],"channels":[1]}`},
		{Name: "string integers", Reply: `{"people":["2","1"],"channels":["1"]}`},
		{Name: "fenced json", Reply: "```json\n{\"people\":[2,1],\"channels\":[1]}\n```"},
		{Name: "prose preamble", Reply: `Sure, here you go: {"people":[2,1],"channels":[1]} hope that helps`},
	}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			chat := &captureChatter{reply: test.Reply}
			ans, err := NewRedactedLLM(redactIndex(), chat, nil).Resolve(context.Background(), "billing", 5)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if len(ans.People) != 2 || ans.People[0].Person.Email != "bob@corp.com" {
				t.Fatalf("top person = %v, want bob@corp.com from the number ranking", ans.People)
			}
		})
	}
}

// TestBuildRedactedPromptOmitsChannelData verifies channel names and topics
// stay out of the redacted prompt even when they carry sensitive tokens.
func TestBuildRedactedPromptOmitsChannelData(t *testing.T) {
	t.Parallel()
	channels := []model.ChannelMatch{{
		Channel: &model.Channel{ID: "acme-deal", Name: "acme-deal", Topic: "jane's acquisition room"},
		Reasons: []string{"billing (topic)"},
	}}

	got := buildRedactedPrompt("billing", nil, channels)
	for _, leak := range []string{"acme", "jane", "acquisition"} {
		if strings.Contains(strings.ToLower(got), leak) {
			t.Errorf("prompt leaked %q:\n%s", leak, got)
		}
	}
	if !strings.Contains(got, "1. matched billing (topic)") {
		t.Errorf("prompt missing numbered matched terms:\n%s", got)
	}
}
