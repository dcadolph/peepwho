package recall

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/model"
)

// recordingSummarizer captures the prompt it was given and returns a canned
// account, or an error when one is set.
type recordingSummarizer struct {
	// prompt is the last user prompt received.
	prompt string
	// system is the last system instruction received.
	system string
	// reply is what to return.
	reply string
	// err is returned instead of a reply when set.
	err error
}

// Chat records the prompt and returns the canned reply.
func (s *recordingSummarizer) Chat(_ context.Context, system, user string) (string, error) {
	s.system, s.prompt = system, user
	if s.err != nil {
		return "", s.err
	}
	return s.reply, nil
}

// archivedFixture returns a resolver holding one kept conversation.
func archivedFixture(t *testing.T) *Resolver {
	t.Helper()
	r := newFixture(t)
	ep, ok := r.store.Episode("slack:C1:1")
	if !ok {
		t.Fatal("fixture episode missing")
	}
	ep.Archive = []episode.Note{
		{Author: "jane@x.com", At: time.Date(2026, 3, 12, 9, 0, 0, 0, time.UTC),
			Text: "the cert renewal keeps failing on staging"},
		{Author: "billy@x.com", At: time.Date(2026, 3, 12, 9, 5, 0, 0, time.UTC),
			Text: "the dns challenge needs the wildcard record, add it and rerun certbot"},
	}
	return r
}

// TestSolutionShowsTheConversation verifies a kept conversation is returned as
// written, attributed to the people who wrote it, with a link to the rest.
func TestSolutionShowsTheConversation(t *testing.T) {
	t.Parallel()
	r := archivedFixture(t)
	ans := r.Resolve(context.Background(), Query{
		Text: "certificate renewal", Person: "jane@x.com", Explain: true,
	})
	if len(ans.Episodes) != 1 || ans.Episodes[0].Solution == nil {
		t.Fatalf("episodes = %+v, want one with its conversation", ans.Episodes)
	}
	sol := ans.Episodes[0].Solution
	if len(sol.Notes) != 2 {
		t.Fatalf("notes = %+v, want both messages", sol.Notes)
	}
	if sol.Notes[1].Author != "Billy Ray" {
		t.Errorf("author = %q, want the person's name", sol.Notes[1].Author)
	}
	if !strings.Contains(sol.Notes[1].Text, "certbot") {
		t.Errorf("note = %q, want the message as written", sol.Notes[1].Text)
	}
	if sol.Notes[0].At != "2026-03-12" {
		t.Errorf("date = %q, want 2026-03-12", sol.Notes[0].At)
	}
	if sol.Source == "" {
		t.Error("no link back to the conversation")
	}
	if sol.Summary != "" {
		t.Errorf("summary = %q, want none without a model", sol.Summary)
	}
}

// TestSolutionOmittedWithoutArchive verifies a conversation whotoask holds only
// a pointer to reports no content, rather than an empty shell.
func TestSolutionOmittedWithoutArchive(t *testing.T) {
	t.Parallel()
	r := newFixture(t)
	ans := r.Resolve(context.Background(), Query{
		Text: "certificate renewal", Person: "jane@x.com", Explain: true,
	})
	if len(ans.Episodes) != 1 {
		t.Fatalf("episodes = %+v, want one", ans.Episodes)
	}
	if ans.Episodes[0].Solution != nil {
		t.Errorf("solution = %+v, want none for a pointer-only conversation", ans.Episodes[0].Solution)
	}
}

// TestSolutionNotRequested verifies content is withheld unless it was asked
// for, so an ordinary recall answer stays a pointer.
func TestSolutionNotRequested(t *testing.T) {
	t.Parallel()
	r := archivedFixture(t)
	ans := r.Resolve(context.Background(), Query{Text: "certificate renewal", Person: "jane@x.com"})
	if ans.Episodes[0].Solution != nil {
		t.Error("content was returned without being asked for")
	}
}

// TestSummaryFromModel verifies the model receives the conversation and an
// instruction not to invent anything, and that its account is returned.
func TestSummaryFromModel(t *testing.T) {
	t.Parallel()
	r := archivedFixture(t)
	sum := &recordingSummarizer{reply: "  Billy added the wildcard DNS record and reran certbot.  "}
	r.SetSummarizer(sum)

	ans := r.Resolve(context.Background(), Query{
		Text: "certificate renewal", Person: "jane@x.com", Explain: true,
	})
	got := ans.Episodes[0].Solution
	if got.Summary != "Billy added the wildcard DNS record and reran certbot." {
		t.Errorf("summary = %q, want it trimmed and returned", got.Summary)
	}
	if !strings.Contains(sum.prompt, "certbot") || !strings.Contains(sum.prompt, "Billy Ray") {
		t.Errorf("prompt = %q, want the conversation and who said what", sum.prompt)
	}
	if !strings.Contains(sum.prompt, "No resolution in this conversation") {
		t.Error("the prompt does not tell the model to admit when nothing was resolved")
	}
	if !strings.Contains(sum.system, "Never invent") {
		t.Errorf("system = %q, want it to forbid invention", sum.system)
	}
}

// TestSummaryFailureKeepsAnswer verifies a model that fails costs the written
// account and nothing else, since the conversation itself is the useful part.
func TestSummaryFailureKeepsAnswer(t *testing.T) {
	t.Parallel()
	r := archivedFixture(t)
	r.SetSummarizer(&recordingSummarizer{err: errors.New("model unreachable")})
	ans := r.Resolve(context.Background(), Query{
		Text: "certificate renewal", Person: "jane@x.com", Explain: true,
	})
	got := ans.Episodes[0].Solution
	if got == nil || len(got.Notes) != 2 {
		t.Fatalf("solution = %+v, want the conversation despite the model failing", got)
	}
	if got.Summary != "" {
		t.Errorf("summary = %q, want none after a failure", got.Summary)
	}
}

// TestSolutionTruncates verifies a long conversation is cut to a readable
// length and says so, rather than dumping a channel.
func TestSolutionTruncates(t *testing.T) {
	t.Parallel()
	r := newFixture(t)
	ep, _ := r.store.Episode("slack:C1:1")
	for i := 0; i < maxSolutionNotes+5; i++ {
		ep.Archive = append(ep.Archive, episode.Note{Author: "jane@x.com", Text: "another message"})
	}
	ans := r.Resolve(context.Background(), Query{
		Text: "certificate renewal", Person: "jane@x.com", Explain: true,
	})
	got := ans.Episodes[0].Solution
	if len(got.Notes) != maxSolutionNotes || !got.Truncated {
		t.Errorf("notes = %d truncated=%v, want %d and a truncation flag",
			len(got.Notes), got.Truncated, maxSolutionNotes)
	}
}

// TestSolutionScopedToAsker verifies content follows the same rule as the
// pointer: a conversation someone was not in is never returned, with or
// without an archive.
func TestSolutionScopedToAsker(t *testing.T) {
	t.Parallel()
	r := archivedFixture(t)
	ans := r.Resolve(context.Background(), Query{
		Text: "certificate renewal", Person: "stranger@x.com", Explain: true,
	})
	if len(ans.Episodes) != 0 {
		t.Errorf("episodes = %+v, want none for someone who was not there", ans.Episodes)
	}
}

// TestNameFallsBackToIdentifier verifies a note by someone the graph never saw
// is still attributed rather than dropped.
func TestNameFallsBackToIdentifier(t *testing.T) {
	t.Parallel()
	r := newFixture(t)
	if got := r.name(model.ID("slack:u404")); got != "slack:u404" {
		t.Errorf("name = %q, want the identifier itself", got)
	}
}

// TestTrimConversationKeepsBothEnds verifies a long conversation keeps its
// opening and its ending. The problem is stated first and settled last, so
// trimming only the tail would cut off the answer this feature exists to show.
func TestTrimConversationKeepsBothEnds(t *testing.T) {
	t.Parallel()
	var notes []episode.Note
	notes = append(notes, episode.Note{Text: "the problem"})
	for i := 0; i < maxSolutionNotes*2; i++ {
		notes = append(notes, episode.Note{Text: "middle chatter"})
	}
	notes = append(notes, episode.Note{Text: "that fixed it"})

	kept, truncated := trimConversation(notes)
	if !truncated {
		t.Fatal("a long conversation was not reported as truncated")
	}
	if len(kept) != maxSolutionNotes {
		t.Fatalf("kept %d messages, want %d", len(kept), maxSolutionNotes)
	}
	if kept[0].Text != "the problem" {
		t.Errorf("first kept = %q, want the problem statement", kept[0].Text)
	}
	if kept[len(kept)-1].Text != "that fixed it" {
		t.Errorf("last kept = %q, want the resolution", kept[len(kept)-1].Text)
	}

	short := []episode.Note{{Text: "one"}, {Text: "two"}}
	if got, truncated := trimConversation(short); truncated || len(got) != 2 {
		t.Errorf("short conversation = (%d, %v), want it untouched", len(got), truncated)
	}
}
