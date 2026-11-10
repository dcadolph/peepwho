package resolve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
)

// fakeChat is a Chatter stub returning a canned reply and recording the prompt.
type fakeChat struct {
	// reply is returned from Chat.
	reply string
	// err is returned from Chat when set.
	err error
	// gotUser captures the user prompt for assertions.
	gotUser string
}

// Chat records the user prompt and returns the canned reply or error.
func (f *fakeChat) Chat(_ context.Context, _, user string) (string, error) {
	f.gotUser = user
	return f.reply, f.err
}

// llmIndex builds a small index with two people and one channel.
func llmIndex() *index.Index {
	ix := index.New()
	ix.Build([]connector.Record{
		{Name: "Jane Roe", Email: "jane@x.com", Title: "Engineer", Topics: []string{"retries"}},
		{Name: "Bob Lee", Email: "bob@x.com", Title: "SRE", Topics: []string{"kafka"}},
		{Kind: connector.KindChannel, Name: "billing", Title: "retries and dunning",
			Members: []string{"jane@x.com", "bob@x.com"}},
	})
	return ix
}

// TestLLMResolveRanksAndSummarizes verifies the model reply drives ranking, the
// summary is composed locally from the grounded top result, and candidates
// reach the prompt.
func TestLLMResolveRanksAndSummarizes(t *testing.T) {
	t.Parallel()
	chat := &fakeChat{
		reply: `{"people":["jane@x.com"],"channels":["billing"]}`,
	}
	ans, err := NewLLM(llmIndex(), chat, nil).Resolve(context.Background(), "retries", 5)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The summary is written locally, so it names only the grounded top result.
	if !strings.Contains(ans.Summary, "Jane Roe") || !strings.Contains(ans.Summary, "#billing") {
		t.Errorf("summary = %q, want a locally grounded recommendation", ans.Summary)
	}
	if len(ans.People) == 0 || ans.People[0].Person.Email != "jane@x.com" {
		t.Errorf("top person = %v, want jane@x.com", ans.People)
	}
	if len(ans.Channels) == 0 || ans.Channels[0].Channel.Name != "billing" {
		t.Errorf("top channel = %v, want billing", ans.Channels)
	}
	if !strings.Contains(chat.gotUser, "jane@x.com") {
		t.Errorf("prompt did not include candidate jane@x.com:\n%s", chat.gotUser)
	}
}

// TestLLMSummaryStaysGrounded verifies a model that names a non-candidate in its
// prose cannot leak that name into the answer: the summary is composed locally,
// so an invented name in the reply is ignored entirely.
func TestLLMSummaryStaysGrounded(t *testing.T) {
	t.Parallel()
	chat := &fakeChat{
		reply: `{"summary":"Talk to Ghost McPhantom, our retries wizard.","people":["jane@x.com"],"channels":["billing"]}`,
	}
	ans, err := NewLLM(llmIndex(), chat, nil).Resolve(context.Background(), "retries", 5)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if strings.Contains(ans.Summary, "Ghost") || strings.Contains(ans.Summary, "McPhantom") {
		t.Errorf("summary leaked an invented name: %q", ans.Summary)
	}
	if !strings.Contains(ans.Summary, "Jane Roe") {
		t.Errorf("summary = %q, want the grounded top candidate", ans.Summary)
	}
}

// TestLLMHonorsAbstention verifies an explicit empty ranking is respected as the
// model saying nothing is relevant, not overridden with the keyword candidates.
func TestLLMHonorsAbstention(t *testing.T) {
	t.Parallel()
	chat := &fakeChat{reply: `{"people":[],"channels":[]}`}
	ans, err := NewLLM(llmIndex(), chat, nil).Resolve(context.Background(), "retries", 5)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(ans.People) != 0 || len(ans.Channels) != 0 {
		t.Errorf("abstention overridden: people=%v channels=%v", ans.People, ans.Channels)
	}
	if ans.Summary != "" {
		t.Errorf("summary = %q, want empty on abstention", ans.Summary)
	}
}

// TestLLMGroundsToCandidates verifies invented people are dropped.
func TestLLMGroundsToCandidates(t *testing.T) {
	t.Parallel()
	chat := &fakeChat{
		reply: `{"summary":"x","people":["ghost@x.com","jane@x.com"],"channels":["nope"]}`,
	}
	ans, err := NewLLM(llmIndex(), chat, nil).Resolve(context.Background(), "retries", 5)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, p := range ans.People {
		if p.Person.Email == "ghost@x.com" {
			t.Fatal("hallucinated person leaked into results")
		}
	}
	found := false
	for _, p := range ans.People {
		if p.Person.Email == "jane@x.com" {
			found = true
		}
	}
	if !found {
		t.Error("real candidate jane@x.com missing from results")
	}
}

// TestLLMChatError verifies a chat failure propagates.
func TestLLMChatError(t *testing.T) {
	t.Parallel()
	chat := &fakeChat{err: errors.New("boom")}
	if _, err := NewLLM(llmIndex(), chat, nil).Resolve(context.Background(), "retries", 5); err == nil {
		t.Fatal("want error from chat failure, got nil")
	}
}

// TestLLMToleratesNonJSON verifies a non-JSON reply keeps keyword order and
// still gets a grounded, locally written summary rather than the raw prose.
func TestLLMToleratesNonJSON(t *testing.T) {
	t.Parallel()
	chat := &fakeChat{reply: "just a sentence"}
	ans, err := NewLLM(llmIndex(), chat, nil).Resolve(context.Background(), "retries", 5)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ans.Summary == "just a sentence" {
		t.Errorf("summary = %q, want a grounded local summary, not the raw reply", ans.Summary)
	}
	if len(ans.People) == 0 {
		t.Error("expected keyword-ordered people on non-JSON reply")
	}
	if !strings.Contains(ans.Summary, ans.People[0].Person.Name) {
		t.Errorf("summary %q does not name the grounded top result", ans.Summary)
	}
}

// TestNewLLMNil verifies the constructor guards nil dependencies.
func TestNewLLMNil(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		Name string
		Run  func()
	}{
		{Name: "nil index", Run: func() { NewLLM(nil, &fakeChat{}, nil) }},
		{Name: "nil chat", Run: func() { NewLLM(index.New(), nil, nil) }},
	} {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Error("expected panic")
				}
			}()
			tc.Run()
		})
	}
}
