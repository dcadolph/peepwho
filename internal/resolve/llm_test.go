package resolve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
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

// TestLLMOffersNoChatterBehindAStatedMatch checks that the model path drops the
// people and channels that only said the word when a stated match leads, as
// the keyword answer does, so a model that picks the first candidate gets the
// keyword answer and is never padded back out with chatter. A chatter leader
// keeps everything, and the channels offered list their members.
func TestLLMOffersNoChatterBehindAStatedMatch(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Name: "Jane Roe", Email: "jane@x.com", Title: "Engineer", Topics: []string{"retries"}},
		{Name: "Bob Lee", Email: "bob@x.com", Title: "SRE", Text: "saw retries spike once"},
		{Name: "Ann Poe", Email: "ann@x.com", Title: "PM", Text: "retries spike in planning"},
		{Kind: connector.KindChannel, Name: "billing", Title: "retries and dunning",
			Members: []string{"jane@x.com", "bob@x.com", "ann@x.com"}},
		{Kind: connector.KindChannel, Name: "ops", Text: "retries pager",
			Members: []string{"bob@x.com", "ann@x.com"}},
	})
	tests := []struct {
		Query        string
		WantPeople   []string
		WantChannels []string
		NotOffered   []string
	}{{ // Test 0: A stated owner and channel lead, so the chatter is never offered.
		Query: "retries", WantPeople: []string{"Jane Roe"}, WantChannels: []string{"billing"},
		NotOffered: []string{"Bob Lee", "Ann Poe", "#ops"},
	}, { // Test 1: Only chatter matches, so the chatter stays.
		Query: "spike", WantPeople: []string{"Ann Poe", "Bob Lee"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			chat := &fakeChat{reply: `{"people":["1"],"channels":["1"]}`}
			got, err := NewLLM(ix, chat, nil).Resolve(context.Background(), test.Query, 5)
			if err != nil {
				t.Fatalf("llm resolve: %v", err)
			}
			keyword, err := NewKeyword(ix).Resolve(context.Background(), test.Query, 5)
			if err != nil {
				t.Fatalf("keyword resolve: %v", err)
			}
			if diff := cmp.Diff(test.WantPeople, matchNames(keyword.People)); diff != "" {
				t.Fatalf("keyword people mismatch, the case proves nothing (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantPeople, matchNames(got.People)); diff != "" {
				t.Errorf("people mismatch (-want +got):\n%s", diff)
			}
			// A channel's member list names people too, so people are looked for
			// only in the candidate people section and channels only in theirs.
			peoplePart, channelPart, _ := strings.Cut(chat.gotUser, "\nCandidate channels:")
			for _, name := range test.WantPeople {
				if !strings.Contains(peoplePart, name) {
					t.Errorf("%s was not offered to the model:\n%s", name, chat.gotUser)
				}
			}
			for _, name := range test.NotOffered {
				part := peoplePart
				if strings.HasPrefix(name, "#") {
					part = channelPart
				}
				if strings.Contains(part, name) {
					t.Errorf("%s was offered to the model:\n%s", name, chat.gotUser)
				}
			}
			var channels []string
			for _, c := range got.Channels {
				channels = append(channels, c.Channel.Name)
				if len(c.TopMembers) == 0 {
					t.Errorf("channel %s offered with no members", c.Channel.Name)
				}
			}
			if diff := cmp.Diff(test.WantChannels, channels, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("channels mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// matchNames lists the names of matched people in order.
func matchNames(ms []model.Match) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Person.Name)
	}
	return out
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

// TestLLMChatFailure verifies a model failure costs the ranking and never the
// answer: the keyword order comes back with a note and the failure attached,
// and only a canceled ask returns the error.
func TestLLMChatFailure(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")
	live := func(*testing.T) context.Context { return context.Background() }
	canceled := func(*testing.T) context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	expired := func(t *testing.T) context.Context {
		t.Helper()
		ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
		t.Cleanup(cancel)
		return ctx
	}
	const summary = "Talk to Jane Roe (Engineer); ask in #billing."
	tests := []struct {
		Want         error
		WantModelErr error
		Ctx          func(*testing.T) context.Context
		ChatErr      error
		Reply        string
		WantSummary  string
		WantPeople   []string
		WantChannels []string
	}{{ // Test 0: A failed model keeps keyword order and says so.
		Ctx: live, ChatErr: errBoom, WantModelErr: errBoom,
		WantSummary: summary + " " + modelFailedNote,
		WantPeople:  []string{"jane@x.com"}, WantChannels: []string{"billing"},
	}, { // Test 1: A model that ran out the clock still yields the answer.
		Ctx: expired, ChatErr: errBoom, WantModelErr: errBoom,
		WantSummary: summary + " " + modelFailedNote,
		WantPeople:  []string{"jane@x.com"}, WantChannels: []string{"billing"},
	}, { // Test 2: A canceled ask returns the error and no answer.
		Ctx: canceled, ChatErr: errBoom, Want: errBoom,
	}, { // Test 3: A model that answers leaves no note and no failure.
		Ctx: live, Reply: `{"people":["jane@x.com"],"channels":["billing"]}`,
		WantSummary: summary,
		WantPeople:  []string{"jane@x.com"}, WantChannels: []string{"billing"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			chat := &fakeChat{reply: test.Reply, err: test.ChatErr}
			ans, err := NewLLM(llmIndex(), chat, nil).Resolve(test.Ctx(t), "retries", 5)
			if !errors.Is(err, test.Want) {
				t.Fatalf("error = %v, want %v", err, test.Want)
			}
			if !errors.Is(ans.ModelErr, test.WantModelErr) {
				t.Errorf("model error = %v, want %v", ans.ModelErr, test.WantModelErr)
			}
			if diff := cmp.Diff(test.WantSummary, ans.Summary); diff != "" {
				t.Errorf("summary mismatch (-want +got):\n%s", diff)
			}
			var people, channels []string
			for _, p := range ans.People {
				people = append(people, p.Person.Email)
			}
			for _, c := range ans.Channels {
				channels = append(channels, c.Channel.Name)
			}
			if diff := cmp.Diff(test.WantPeople, people, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("people mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantChannels, channels, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("channels mismatch (-want +got):\n%s", diff)
			}
		})
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
