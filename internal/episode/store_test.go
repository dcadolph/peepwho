package episode

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/model"
)

// fixedNow pins the clock so recency decay is deterministic.
var fixedNow = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// newTestStore returns a store with a pinned clock.
func newTestStore() *Store {
	s := New()
	s.now = func() time.Time { return fixedNow }
	return s
}

// testEpisode returns an episode with the given id, participants, and age.
func testEpisode(id string, daysAgo int, participants ...model.ID) Episode {
	return Episode{
		ID:           id,
		Source:       "slack",
		Kind:         KindThread,
		Place:        "payments",
		Participants: participants,
		Occurred:     fixedNow.AddDate(0, 0, -daysAgo),
		Permalink:    "https://acme.slack.com/archives/C1/p1",
		Messages:     4,
	}
}

// withBody attaches the conversation text an episode is indexed by.
func withBody(ep Episode, body string) Episode {
	ep.Body = body
	return ep
}

// TestSearchScopesToParticipant verifies that a person-scoped query returns
// only episodes that person took part in, which is the whole access model for
// personal recall.
func TestSearchScopesToParticipant(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	s.Add(withBody(testEpisode("a", 10, "me@x.com", "billy@x.com"), "certificate renewal expired"))
	s.Add(withBody(testEpisode("b", 10, "someone@x.com"), "certificate renewal expired"))

	got := s.Search(Query{Text: "certificate renewal", Person: "me@x.com"})
	if len(got) != 1 || got[0].Episode.ID != "a" {
		t.Fatalf("scoped search = %+v, want only episode a", got)
	}

	if all := s.Search(Query{Text: "certificate renewal"}); len(all) != 2 {
		t.Errorf("unscoped search = %d results, want 2", len(all))
	}
	if none := s.Search(Query{Text: "certificate", Person: "nobody@x.com"}); len(none) != 0 {
		t.Errorf("search for a stranger = %+v, want no results", none)
	}
}

// TestSearchRanking verifies coverage and recency both move the ranking, so a
// thread matching the whole question beats one matching part of it, and a
// recent match beats an old one.
func TestSearchRanking(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name    string
		Query   string
		Add     func(s *Store)
		WantTop string
	}{{ // Test 0: Covering more of the question wins.
		Name:  "coverage",
		Query: "kafka consumer lag",
		Add: func(s *Store) {
			s.Add(withBody(testEpisode("full", 30, "me@x.com"), "kafka consumer lag rebalance"))
			s.Add(withBody(testEpisode("partial", 30, "me@x.com"), "kafka broker restart"))
		},
		WantTop: "full",
	}, { // Test 1: With equal coverage, the recent episode wins.
		Name:  "recency",
		Query: "kafka lag",
		Add: func(s *Store) {
			s.Add(withBody(testEpisode("recent", 10, "me@x.com"), "kafka lag"))
			s.Add(withBody(testEpisode("old", 900, "me@x.com"), "kafka lag"))
		},
		WantTop: "recent",
	}, { // Test 2: Repetition saturates, so a long noisy thread does not win.
		Name:  "saturation",
		Query: "kafka lag",
		Add: func(s *Store) {
			s.Add(withBody(testEpisode("noisy", 30, "me@x.com"),
				"kafka kafka kafka kafka kafka kafka kafka kafka"))
			s.Add(withBody(testEpisode("onpoint", 30, "me@x.com"), "kafka lag"))
		},
		WantTop: "onpoint",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			s := newTestStore()
			test.Add(s)
			got := s.Search(Query{Text: test.Query, Person: "me@x.com"})
			if len(got) == 0 {
				t.Fatalf("no results for %q", test.Query)
			}
			if got[0].Episode.ID != test.WantTop {
				t.Errorf("top = %q, want %q (all: %+v)", got[0].Episode.ID, test.WantTop, got)
			}
		})
	}
}

// TestSearchEmptyQuery verifies a query with nothing but stopwords matches
// nothing rather than everything.
func TestSearchEmptyQuery(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	s.Add(withBody(testEpisode("a", 1, "me@x.com"), "kafka lag"))
	if got := s.Search(Query{Text: "how do I", Person: "me@x.com"}); len(got) != 0 {
		t.Errorf("stopword-only query = %+v, want no results", got)
	}
}

// TestAddReplacesEpisode verifies re-indexing the same episode updates it in
// place and leaves no stale postings behind.
func TestAddReplacesEpisode(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	s.Add(withBody(testEpisode("a", 10, "me@x.com"), "certificate renewal"))
	s.Add(withBody(testEpisode("a", 10, "me@x.com"), "database migration"))

	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1", s.Len())
	}
	if got := s.Search(Query{Text: "certificate", Person: "me@x.com"}); len(got) != 0 {
		t.Errorf("stale term still matches: %+v", got)
	}
	if got := s.Search(Query{Text: "migration", Person: "me@x.com"}); len(got) != 1 {
		t.Errorf("new term = %+v, want one hit", got)
	}
	if ids := s.byParticipant["me@x.com"]; len(ids) != 1 {
		t.Errorf("participant links = %v, want one", ids)
	}
}

// TestOthers verifies the asker is left out of the people an answer names.
func TestOthers(t *testing.T) {
	t.Parallel()
	ep := testEpisode("a", 1, "me@x.com", "billy@x.com", "carol@x.com")
	want := []model.ID{"billy@x.com", "carol@x.com"}
	if diff := cmp.Diff(want, ep.Others("me@x.com"), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
	if !ep.Involves("me@x.com") || ep.Involves("nobody@x.com") {
		t.Error("Involves disagrees with the participant list")
	}
}

// TestPurge verifies retention drops old episodes and that clearing the
// archive leaves the pointer behind.
func TestPurge(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	old := testEpisode("old", 400, "me@x.com")
	old.Archive = []Note{{Author: "me@x.com", At: fixedNow, Text: "the fix"}}
	old.Body = "kafka lag"
	s.Add(old)
	s.Add(withBody(testEpisode("new", 10, "me@x.com"), "kafka lag"))

	if n := s.PurgeArchive(); n != 1 {
		t.Errorf("PurgeArchive = %d, want 1", n)
	}
	if ep, _ := s.Episode("old"); ep.Archived() {
		t.Error("archive survived the purge")
	}
	if n := s.PurgeBefore(fixedNow.AddDate(0, 0, -100)); n != 1 {
		t.Errorf("PurgeBefore = %d, want 1", n)
	}
	if s.Len() != 1 {
		t.Errorf("Len after purge = %d, want 1", s.Len())
	}
	if got := s.Search(Query{Text: "kafka lag", Person: "me@x.com"}); len(got) != 1 {
		t.Errorf("search after purge = %+v, want one hit", got)
	}
}

// TestAddKeepsArchiveOnReindex verifies a routine re-index without the archive
// does not throw away conversation content already kept. The source may no
// longer serve those messages, so only an explicit prune deletes them.
func TestAddKeepsArchiveOnReindex(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	kept := testEpisode("a", 10, "me@x.com")
	kept.Archive = []Note{{Author: "billy@x.com", At: fixedNow, Text: "bump the cert"}}
	kept.Body = "certificate renewal"
	s.Add(kept)

	// The same conversation, seen again by a run that was not keeping words.
	s.Add(withBody(testEpisode("a", 10, "me@x.com"), "certificate renewal"))

	ep, ok := s.Episode("a")
	if !ok || !ep.Archived() {
		t.Fatalf("episode = %+v, want its content kept through a re-index", ep)
	}
	if ep.Archive[0].Text != "bump the cert" {
		t.Errorf("archive = %+v, want the original message", ep.Archive)
	}

	// A run that does carry content replaces it, so corrections land.
	fresh := testEpisode("a", 10, "me@x.com")
	fresh.Archive = []Note{{Author: "billy@x.com", At: fixedNow, Text: "use certbot instead"}}
	fresh.Body = "certificate renewal"
	s.Add(fresh)
	if ep, _ := s.Episode("a"); ep.Archive[0].Text != "use certbot instead" {
		t.Errorf("archive = %+v, want the newer content", ep.Archive)
	}

	// Pruning is the one way content goes away.
	s.PurgeArchive()
	if ep, _ := s.Episode("a"); ep.Archived() {
		t.Error("prune did not clear the content")
	}
}

// TestReindexKeepsArchiveParticipants verifies a later run without the archive
// keeps the people the archive contributed. Replies are read only while
// archiving, so a plain re-index reports just the participants visible without
// them, and dropping the rest would leave the retained conversation unreachable
// by the person who actually solved it.
func TestReindexKeepsArchiveParticipants(t *testing.T) {
	t.Parallel()
	s := New()
	// The archiving run saw the thread starter and the expert who replied.
	s.Add(Episode{
		ID: "slack:C1:1", Source: "slack", Kind: KindThread, Place: "billing",
		Participants: []model.ID{"asker@x.com", "expert@x.com"},
		Archive: []Note{
			{Author: "expert@x.com", Text: "raise the retry ceiling and redeploy"},
		},
	})
	if !s.HasPerson("expert@x.com") {
		t.Fatal("setup: the expert should be reachable after the archiving run")
	}

	// A later plain run sees only the thread parent's author.
	s.Add(Episode{
		ID: "slack:C1:1", Source: "slack", Kind: KindThread, Place: "billing",
		Participants: []model.ID{"asker@x.com"},
	})

	ep, ok := s.Episode("slack:C1:1")
	if !ok {
		t.Fatal("episode disappeared on re-index")
	}
	if len(ep.Archive) != 1 {
		t.Errorf("archive = %+v, want it kept", ep.Archive)
	}
	if !s.HasPerson("expert@x.com") {
		t.Error("the expert who wrote the retained answer can no longer reach it")
	}
	if !s.HasPerson("asker@x.com") {
		t.Error("the thread starter lost their link")
	}
	hits := s.Search(Query{Text: "retry ceiling", Person: "expert@x.com"})
	if len(hits) != 1 {
		t.Errorf("expert's own recall returned %d conversations, want 1", len(hits))
	}
}
