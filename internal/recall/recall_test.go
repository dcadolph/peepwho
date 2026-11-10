package recall

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// newFixture builds an index of two people and an episode store holding one
// conversation between them, which is the shape every case below asks about.
func newFixture(t *testing.T) *Resolver {
	t.Helper()
	ix := index.New()
	ix.Build([]connector.Record{
		{
			Kind: connector.KindPerson, Source: "slack", PersonID: "slack:U1",
			Name: "Jane Roe", Email: "jane@x.com", Title: "Staff Engineer",
		},
		{
			Kind: connector.KindPerson, Source: "slack", PersonID: "slack:U2",
			Name: "Billy Ray", Email: "billy@x.com", Title: "SRE",
		},
	})
	store := episode.New()
	store.Add(episode.Episode{
		ID:           "slack:C1:1",
		Source:       "slack",
		Kind:         episode.KindThread,
		Place:        "infra",
		Participants: []model.ID{"jane@x.com", "billy@x.com"},
		Occurred:     time.Now().AddDate(0, 0, -60),
		Permalink:    "https://acme.slack.com/archives/C1/p1",
		Messages:     6,
		Body:         "the certificate renewal keeps failing on staging",
	})
	return New(store, ix)
}

// TestResolveNamesTheHelper verifies the answer names the other person, not
// the asker, and carries the pointer back to the conversation.
func TestResolveNamesTheHelper(t *testing.T) {
	t.Parallel()
	r := newFixture(t)
	ans := r.Resolve(context.Background(), Query{Text: "certificate renewal", Person: "jane@x.com"})
	if len(ans.Episodes) != 1 {
		t.Fatalf("episodes = %+v, want one", ans.Episodes)
	}
	ep := ans.Episodes[0]
	want := []Person{{Name: "Billy Ray", Email: "billy@x.com", Title: "SRE"}}
	if diff := cmp.Diff(want, ep.People); diff != "" {
		t.Errorf("people mismatch (-want +got):\n%s", diff)
	}
	if ep.Place != "infra" || ep.Permalink == "" || ep.Messages != 6 {
		t.Errorf("episode = %+v, want the infra thread with a link", ep)
	}
	if ep.Strength <= 0 || ep.Strength > 1 {
		t.Errorf("strength = %v, want a value in (0,1]", ep.Strength)
	}
	if ep.LinkMayHaveExpired {
		t.Error("link flagged as expired with no horizon set")
	}
}

// TestResolveScopesToAsker verifies someone who was not in the conversation
// gets nothing, so the index cannot be used to read another person's history.
func TestResolveScopesToAsker(t *testing.T) {
	t.Parallel()
	r := newFixture(t)
	ans := r.Resolve(context.Background(), Query{Text: "certificate renewal", Person: "stranger@x.com"})
	if len(ans.Episodes) != 0 {
		t.Errorf("episodes = %+v, want none for a stranger", ans.Episodes)
	}
	if ans.Scope.Episodes != 1 || len(ans.Scope.Sources) != 1 || ans.Scope.Note == "" {
		t.Errorf("scope = %+v, want it to state what was searched", ans.Scope)
	}
}

// TestHorizonFlagsOldLinks verifies a conversation older than the configured
// horizon is marked, since the source may no longer serve it.
func TestHorizonFlagsOldLinks(t *testing.T) {
	t.Parallel()
	r := newFixture(t)
	r.SetHorizon(30 * 24 * time.Hour)
	ans := r.Resolve(context.Background(), Query{Text: "certificate renewal", Person: "jane@x.com"})
	if len(ans.Episodes) != 1 || !ans.Episodes[0].LinkMayHaveExpired {
		t.Errorf("episodes = %+v, want the old link flagged", ans.Episodes)
	}
}

// TestWhoResolvesSourceIdentifiers verifies the asker can be named by email or
// by the identifier a source knows them as, which is how the Slack bot
// recognizes whoever typed the command.
func TestWhoResolvesSourceIdentifiers(t *testing.T) {
	t.Parallel()
	r := newFixture(t)
	tests := []struct {
		Hint string
		Want model.ID
	}{
		{Hint: "jane@x.com", Want: "jane@x.com"},
		{Hint: "slack:U1", Want: "jane@x.com"},
		{Hint: "  jane@x.com  ", Want: "jane@x.com"},
		{Hint: "", Want: ""},
		{Hint: "nobody@x.com", Want: "nobody@x.com"},
	}
	for _, test := range tests {
		if got := r.Who(test.Hint); got != test.Want {
			t.Errorf("Who(%q) = %q, want %q", test.Hint, got, test.Want)
		}
	}
}

// TestKnownSeparatesEmptyCases verifies whotoask can tell "you were in no
// indexed conversation" from "nothing is indexed", which is what the empty
// answer explains to the user.
func TestKnownSeparatesEmptyCases(t *testing.T) {
	t.Parallel()
	r := newFixture(t)
	if !r.Known("jane@x.com") {
		t.Error("Known(jane) = false, want true")
	}
	if r.Known("stranger@x.com") {
		t.Error("Known(stranger) = true, want false")
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1", r.Len())
	}
}

// TestUnknownParticipantStillNamed verifies a participant the graph never saw
// is reported by identifier rather than dropped from the answer.
func TestUnknownParticipantStillNamed(t *testing.T) {
	t.Parallel()
	ix := index.New()
	store := episode.New()
	store.Add(episode.Episode{
		ID: "slack:C1:1", Source: "slack", Kind: episode.KindThread, Place: "infra",
		Participants: []model.ID{"me@x.com", "slack:u9"},
		Occurred:     time.Now().AddDate(0, 0, -5),
		Body:         "kafka lag",
	})
	ans := New(store, ix).Resolve(context.Background(), Query{Text: "kafka lag", Person: "me@x.com"})
	if len(ans.Episodes) != 1 || len(ans.Episodes[0].People) != 1 {
		t.Fatalf("episodes = %+v, want one with one other person", ans.Episodes)
	}
	if got := ans.Episodes[0].People[0].ID; got != "slack:u9" {
		t.Errorf("unnamed participant = %q, want slack:u9", got)
	}
}

// TestIdentityJoinReachesOldConversations verifies a conversation recorded
// under a source handle becomes findable by the person once a later index
// links that handle to their email. Sources are indexed one at a time, so this
// is the normal case, not an edge case.
func TestIdentityJoinReachesOldConversations(t *testing.T) {
	t.Parallel()
	store := episode.New()
	store.Add(episode.Episode{
		ID: "github:corp/billing:412", Source: "github", Kind: episode.KindChange,
		Place: "corp/billing", Title: "Fix billing retry backoff",
		// Recorded before anyone knew whose login this was.
		Participants: []model.ID{"github:amalone"},
		Occurred:     time.Now().AddDate(0, 0, -30),
		Body:         "billing retry backoff",
	})

	// Before the join, the person cannot reach their own work.
	bare := index.New()
	if got := New(store, bare).Resolve(context.Background(), Query{
		Text: "billing retry", Person: "angela@corp.com",
	}); len(got.Episodes) != 0 {
		t.Fatalf("episodes = %+v, want none before the identity join", got.Episodes)
	}

	// A later index links the handle to the person.
	joined := index.New()
	joined.Build([]connector.Record{{
		Kind: connector.KindPerson, Source: "github",
		PersonID: "github:amalone", Name: "Angela Malone", Email: "angela@corp.com",
	}})

	ans := New(store, joined).Resolve(context.Background(), Query{
		Text: "billing retry", Person: "angela@corp.com",
	})
	if len(ans.Episodes) != 1 {
		t.Fatalf("episodes = %+v, want the change found after the join", ans.Episodes)
	}
	if ans.Episodes[0].Source != "github" {
		t.Errorf("source = %q, want github", ans.Episodes[0].Source)
	}
}
