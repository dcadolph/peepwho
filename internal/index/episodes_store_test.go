package index

import (
	"testing"

	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/model"
)

// TestCanonicalizeStoreRelinksWithoutStaleKeys verifies that re-resolving a
// stored conversation moves it fully onto the joined person and leaves no
// reverse-index entry under the old identifier. The store's episode and the
// copy passed to canonicalization share a backing array unless the code takes
// care, so a careless filter-in-place would leave the old handle still
// answering for the conversation after the join.
func TestCanonicalizeStoreRelinksWithoutStaleKeys(t *testing.T) {
	t.Parallel()
	ix := New()
	// Alice's GitHub login is the same person as her email.
	ix.Alias("github:alice", "alice@x.com")

	store := episode.New()
	store.Add(episode.Episode{
		ID: "github:acme/billing:1", Source: "github", Kind: episode.KindChange,
		Place: "acme/billing", Participants: []model.ID{"github:alice", "github:bob"},
	})
	if !store.HasPerson("github:alice") {
		t.Fatal("setup: the episode should start under the github login")
	}

	ix.CanonicalizeStore(store)

	// The conversation now answers for the joined person.
	if !store.HasPerson("alice@x.com") {
		t.Error("after canonicalization the joined email cannot reach the conversation")
	}
	// And no longer for the handle that was merged away.
	if store.HasPerson("github:alice") {
		t.Error("the old github login still answers for the conversation after the join")
	}
	// bob was never aliased, so he is untouched.
	if !store.HasPerson("github:bob") {
		t.Error("an unaliased participant lost their link")
	}

	// The stored episode carries the canonical ids, once each, in order.
	ep, ok := store.Episode("github:acme/billing:1")
	if !ok {
		t.Fatal("episode disappeared")
	}
	want := []model.ID{"alice@x.com", "github:bob"}
	if len(ep.Participants) != len(want) {
		t.Fatalf("participants = %v, want %v", ep.Participants, want)
	}
	for i := range want {
		if ep.Participants[i] != want[i] {
			t.Errorf("participant %d = %q, want %q", i, ep.Participants[i], want[i])
		}
	}
}

// TestCanonicalizeStoreDeduplicates verifies two identifiers that collapse onto
// one person leave a single reverse-index entry, not a doubled one.
func TestCanonicalizeStoreDeduplicates(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Alias("github:alice", "alice@x.com")
	ix.Alias("slack:U1", "alice@x.com")

	store := episode.New()
	store.Add(episode.Episode{
		ID: "slack:C1:1", Source: "slack", Kind: episode.KindThread, Place: "billing",
		Participants: []model.ID{"github:alice", "slack:U1", "alice@x.com"},
	})

	ix.CanonicalizeStore(store)

	ep, _ := store.Episode("slack:C1:1")
	if len(ep.Participants) != 1 || ep.Participants[0] != "alice@x.com" {
		t.Fatalf("participants = %v, want the one joined person", ep.Participants)
	}
	// The conversation counts once, not three times, for the person.
	hits := store.Search(episode.Query{Text: "billing", Person: "alice@x.com"})
	if len(hits) != 1 {
		t.Errorf("recall returned %d conversations, want 1", len(hits))
	}
}
