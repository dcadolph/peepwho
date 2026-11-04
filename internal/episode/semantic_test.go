package episode

import (
	"testing"
)

// TestSearchSemantic verifies meaning-based recall ranks the closest
// conversation first and still refuses to leave the asker's own history.
func TestSearchSemantic(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	s.Add(withBody(testEpisode("close", 30, "me@x.com"), "kafka lag"))
	s.Add(withBody(testEpisode("far", 30, "me@x.com"), "billing invoice"))
	s.Add(withBody(testEpisode("theirs", 30, "someone@x.com"), "kafka lag"))
	s.SetVector("close", []float32{1, 0, 0})
	s.SetVector("far", []float32{0, 1, 0})
	s.SetVector("theirs", []float32{1, 0, 0})

	got := s.SearchSemantic([]float32{0.9, 0.1, 0}, Query{Person: "me@x.com"})
	if len(got) != 2 {
		t.Fatalf("results = %+v, want the asker's two conversations", got)
	}
	if got[0].Episode.ID != "close" {
		t.Errorf("top = %q, want close", got[0].Episode.ID)
	}
	for _, r := range got {
		if r.Episode.ID == "theirs" {
			t.Error("a conversation the asker was not in came back")
		}
	}
	if got[0].Strength <= 0 {
		t.Errorf("strength = %v, want above zero", got[0].Strength)
	}
}

// TestSearchSemanticWithoutVectors verifies meaning-based recall returns
// nothing rather than guessing when no conversation was embedded.
func TestSearchSemanticWithoutVectors(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	s.Add(withBody(testEpisode("a", 1, "me@x.com"), "kafka lag"))
	if s.HasVectors() {
		t.Fatal("HasVectors = true with nothing embedded")
	}
	if got := s.SearchSemantic([]float32{1, 0}, Query{Person: "me@x.com"}); len(got) != 0 {
		t.Errorf("results = %+v, want none", got)
	}
	s.SetVector("a", []float32{1, 0})
	if !s.HasVectors() {
		t.Error("HasVectors = false after embedding one conversation")
	}
	if got := s.SearchSemantic(nil, Query{Person: "me@x.com"}); len(got) != 0 {
		t.Errorf("results for an empty query vector = %+v, want none", got)
	}
}

// TestVectorSurvivesReindex verifies re-indexing a conversation keeps its
// embedding. Embeddings can only be computed while the text is in hand, so a
// routine index run without --embed must not throw them away: a slightly stale
// vector costs a little ranking precision, while a lost one silently breaks
// meaning-based recall until every conversation is embedded again. A later
// embed run overwrites the vector, so staleness corrects itself.
func TestVectorSurvivesReindex(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	s.Add(withBody(testEpisode("a", 1, "me@x.com"), "kafka lag"))
	s.SetVector("a", []float32{1, 0})

	s.Add(withBody(testEpisode("a", 1, "me@x.com"), "kafka lag"))
	if _, ok := s.Vector("a"); !ok {
		t.Error("the vector was dropped by a re-index that carried no embedding")
	}

	s.SetVector("a", []float32{0, 1})
	if v, _ := s.Vector("a"); v[1] != 1 {
		t.Errorf("vector = %v, want the newer embedding to win", v)
	}

	// Forgetting a conversation outright still takes its vector with it.
	s.PurgeBefore(fixedNow.AddDate(0, 0, 1))
	if _, ok := s.Vector("a"); ok {
		t.Error("a purged conversation left its vector behind")
	}
}
