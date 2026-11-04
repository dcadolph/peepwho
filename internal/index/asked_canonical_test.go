package index

import (
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/model"
)

// TestCanonicalizeResolvesTheAsker pins the miscount an un-canonicalized asker
// produces.
//
// Participants are rewritten to canonical ids on every join. An asker left
// under its source-native id then matches none of them, so Answered excludes
// nobody and returns the whole list: the person who asked the question is
// counted among the people who answered it. Any scoring that reads answer-hood
// inherits that silently, which is why it is pinned here rather than wherever
// the scoring lands.
func TestCanonicalizeResolvesTheAsker(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Ann Lee", Email: "ann@corp.com",
			AltIDs: []string{"github:ann"}, Topics: []string{"billing"}, Source: "github"},
		{Kind: connector.KindPerson, Name: "Bo Ray", Email: "bo@corp.com",
			Topics: []string{"billing"}, Source: "git"},
	})
	ix.AutoJoin()
	ix.Canonicalize()

	eps := []episode.Episode{{
		ID:           "gh:1",
		Source:       "github",
		Kind:         episode.KindThread,
		Asked:        "github:ann",
		Participants: []model.ID{"github:ann", "bo@corp.com"},
	}}
	ix.CanonicalizeEpisodes(eps)

	if eps[0].Asked == "github:ann" {
		t.Fatal("the asker was left under its source-native id")
	}
	answered := eps[0].Answered()
	for _, id := range answered {
		if id == eps[0].Asked {
			t.Errorf("Answered = %v, which includes the asker %q: they answered themselves",
				answered, eps[0].Asked)
		}
	}
	if len(answered) != 1 {
		t.Errorf("Answered = %v, want only the person who replied", answered)
	}
}

// TestCanonicalizeStoreResolvesTheAsker pins the same property for an episode
// already on disk, which reaches canonicalization through Relink rather than
// through the slice.
func TestCanonicalizeStoreResolvesTheAsker(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Ann Lee", Email: "ann@corp.com",
			AltIDs: []string{"github:ann"}, Topics: []string{"billing"}, Source: "github"},
		{Kind: connector.KindPerson, Name: "Bo Ray", Email: "bo@corp.com",
			Topics: []string{"billing"}, Source: "git"},
	})
	ix.AutoJoin()
	ix.Canonicalize()

	store := episode.New()
	store.Add(episode.Episode{
		ID: "gh:1", Source: "github", Kind: episode.KindThread,
		Asked:        "github:ann",
		Participants: []model.ID{"github:ann", "bo@corp.com"},
	})
	ix.CanonicalizeStore(store)

	for _, ep := range store.All() {
		if ep.Asked == "github:ann" {
			t.Error("a stored episode kept its source-native asker")
		}
		for _, id := range ep.Answered() {
			if id == ep.Asked {
				t.Errorf("stored episode credits the asker %q with answering", ep.Asked)
			}
		}
	}
}
