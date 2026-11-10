package episode

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/model"
)

// storeWith fills a store with n episodes, each with its own vocabulary, so the
// postings hold far more terms than any single episode contributed.
func storeWith(t *testing.T, n int) *Store {
	t.Helper()
	s := New()
	for i := range n {
		s.Add(Episode{
			ID:       fmt.Sprintf("ep%d", i),
			Title:    fmt.Sprintf("subject%d trouble", i),
			Body:     fmt.Sprintf("word%da word%db word%dc", i, i, i),
			Occurred: time.Now(),
		})
	}
	return s
}

// TestForgettingVisitsOnlyItsOwnTerms checks removing an episode does not walk
// the whole vocabulary. It used to, which made re-indexing cost the number of
// episodes times the size of the vocabulary: quadratic, and felt as a long
// silent pause on every re-run.
func TestForgettingVisitsOnlyItsOwnTerms(t *testing.T) {
	t.Parallel()
	s := storeWith(t, 200)
	before := len(s.postings)

	// The episode being removed knows its own terms.
	mine := len(s.termsOf["ep7"])
	if mine == 0 {
		t.Fatal("the episode recorded none of its own terms")
	}
	if mine >= before {
		t.Fatalf("one episode claims %d terms of a %d term vocabulary, which is not a subset",
			mine, before)
	}
	s.forget(s.episodes["ep7"])

	if _, ok := s.termsOf["ep7"]; ok {
		t.Error("the forgotten episode still claims terms")
	}
	// Its own terms are gone from the postings and nobody else's are.
	for term, posting := range s.postings {
		if _, ok := posting["ep7"]; ok {
			t.Errorf("term %q still points at the forgotten episode", term)
		}
	}
	if len(s.postings) >= before {
		t.Errorf("vocabulary is %d after forgetting, was %d: its terms were not removed",
			len(s.postings), before)
	}
	// Everybody else is untouched.
	if len(s.termsOf) != 199 {
		t.Errorf("%d episodes still know their terms, want 199", len(s.termsOf))
	}
}

// TestForgettingWorksAfterAReload checks a store restored from disk, which has
// postings but no record of which terms came from where, still removes an
// episode completely.
func TestForgettingWorksAfterAReload(t *testing.T) {
	t.Parallel()
	s := storeWith(t, 20)
	// A store as it arrives from disk, before its terms are derived.
	s.termsOf = nil
	s.forget(s.episodes["ep3"])
	for term, posting := range s.postings {
		if _, ok := posting["ep3"]; ok {
			t.Errorf("term %q still points at the forgotten episode after a reload", term)
		}
	}
}

// TestForgetPersonSparesBystander purges a person whose canonical id sits in
// the participants while their notes still carry an old source-native id, the
// state any identity join leaves behind. The purge takes the whole identity
// set in one pass; judged one identifier at a time, the canonical pass edited
// the episode down to the bystander alone, and the alias pass then read it as
// a one-person conversation and destroyed the bystander's episode and notes.
func TestForgetPersonSparesBystander(t *testing.T) {
	t.Parallel()
	s := New()
	s.Add(Episode{
		ID: "slack:C1:1", Source: "slack", Kind: KindThread, Place: "billing",
		Participants: []model.ID{"alice@corp.com", "bob@corp.com"},
		Occurred:     time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Archive: []Note{
			{Author: "slack:UALICE", Text: "rotate the key first"},
			{Author: "bob@corp.com", Text: "then bump the deployment"},
		},
	})

	dropped, edited := s.ForgetPerson([]model.ID{"alice@corp.com", "slack:UALICE"})

	if dropped != 0 || edited != 1 {
		t.Fatalf("dropped, edited = %d, %d; want 0, 1", dropped, edited)
	}
	ep, ok := s.Episode("slack:C1:1")
	if !ok {
		t.Fatal("the bystander's conversation was destroyed")
	}
	if diff := cmp.Diff([]model.ID{"bob@corp.com"}, ep.Participants, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("participants (-want +got):\n%s", diff)
	}
	for _, n := range ep.Archive {
		if n.Author == "slack:UALICE" {
			t.Errorf("a purged identity's note survived: %q", n.Text)
		}
	}
	if len(ep.Archive) != 1 || ep.Archive[0].Author != "bob@corp.com" {
		t.Errorf("bystander's notes = %+v, want only bob's", ep.Archive)
	}
}

// TestForgetPersonDropsOnlyWhenAlone verifies the drop rule: an episode goes
// only when nobody outside the purged identity set appears in it, as a
// participant or as a note author.
func TestForgetPersonDropsOnlyWhenAlone(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Participants []model.ID
		Authors      []model.ID
		Purge        []model.ID
		WantDropped  int
		WantEdited   int
		WantKept     bool
	}{{ // Test 0: Alone as participant and author under two identities: dropped.
		Participants: []model.ID{"alice@corp.com"},
		Authors:      []model.ID{"slack:UALICE"},
		Purge:        []model.ID{"alice@corp.com", "slack:UALICE"},
		WantDropped:  1, WantKept: false,
	}, { // Test 1: Another participant present: edited, never dropped.
		Participants: []model.ID{"alice@corp.com", "bob@corp.com"},
		Purge:        []model.ID{"alice@corp.com"},
		WantEdited:   1, WantKept: true,
	}, { // Test 2: Sole participant, but a note by somebody else: the episode
		// holds another person's words and must survive.
		Participants: []model.ID{"alice@corp.com"},
		Authors:      []model.ID{"bob@corp.com"},
		Purge:        []model.ID{"alice@corp.com"},
		WantEdited:   1, WantKept: true,
	}, { // Test 3: Not involved at all: untouched.
		Participants: []model.ID{"bob@corp.com"},
		Purge:        []model.ID{"alice@corp.com"},
		WantKept:     true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			s := New()
			notes := make([]Note, 0, len(test.Authors))
			for _, a := range test.Authors {
				notes = append(notes, Note{Author: a, Text: "note"})
			}
			s.Add(Episode{
				ID: "ep", Participants: test.Participants, Archive: notes,
				Occurred: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			})
			dropped, edited := s.ForgetPerson(test.Purge)
			if dropped != test.WantDropped || edited != test.WantEdited {
				t.Errorf("dropped, edited = %d, %d; want %d, %d",
					dropped, edited, test.WantDropped, test.WantEdited)
			}
			if _, ok := s.Episode("ep"); ok != test.WantKept {
				t.Errorf("episode kept = %t, want %t", ok, test.WantKept)
			}
		})
	}
}
