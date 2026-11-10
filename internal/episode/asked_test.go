package episode

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/model"
)

// TestAnswered verifies who an episode reports as having answered, which is the
// half of the ask that carries knowledge. An episode with no asker must report
// nobody rather than everyone who was present.
func TestAnswered(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name         string
		Asked        model.ID
		Participants []model.ID
		WantAnswered []model.ID
	}{{ // Test 0: A thread's repliers are the answerers, and the asker is not.
		Name:         "thread",
		Asked:        "ann@x.com",
		Participants: []model.ID{"ann@x.com", "bo@x.com", "cy@x.com"},
		WantAnswered: []model.ID{"bo@x.com", "cy@x.com"},
	}, { // Test 1: With no asker nobody answered, so the participants are not
		// handed back as if they had.
		Name:         "no asker",
		Participants: []model.ID{"ann@x.com", "bo@x.com", "cy@x.com"},
		WantAnswered: nil,
	}, { // Test 2: An asker whose question drew no reply has no answerers, which
		// is a different fact from having no asker and reads the same here.
		Name:         "asker alone",
		Asked:        "ann@x.com",
		Participants: []model.ID{"ann@x.com"},
		WantAnswered: nil,
	}, { // Test 3: An asker listed somewhere other than first is still excluded,
		// since the field states the ask rather than a slice position.
		Name:         "asker listed last",
		Asked:        "cy@x.com",
		Participants: []model.ID{"ann@x.com", "bo@x.com", "cy@x.com"},
		WantAnswered: []model.ID{"ann@x.com", "bo@x.com"},
	}, { // Test 4: An asker the participant list never names leaves everyone
		// present as an answerer, which is what a source reporting a reporter
		// outside the conversation produces.
		Name:         "asker not a participant",
		Asked:        "dee@x.com",
		Participants: []model.ID{"ann@x.com", "bo@x.com"},
		WantAnswered: []model.ID{"ann@x.com", "bo@x.com"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			ep := Episode{ID: "a", Asked: test.Asked, Participants: test.Participants}
			got := ep.Answered()
			if diff := cmp.Diff(test.WantAnswered, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Answered mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestAskedSurvivesSaveLoad verifies who asked is still known after a store is
// written and read back. The asker is interned alongside the participants, so a
// packing mistake would restore the wrong person rather than none.
func TestAskedSurvivesSaveLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name         string
		Asked        model.ID
		Participants []model.ID
		WantAsked    model.ID
		WantAnswered []model.ID
	}{{ // Test 0: A thread's asker is the same person after the round trip.
		Name:         "asked persists",
		Asked:        "ann@x.com",
		Participants: []model.ID{"ann@x.com", "bo@x.com"},
		WantAsked:    "ann@x.com",
		WantAnswered: []model.ID{"bo@x.com"},
	}, { // Test 1: An episode with no asker does not gain one, which the id table
		// offset is what prevents: index zero is a real person.
		Name:         "no asker stays empty",
		Participants: []model.ID{"ann@x.com", "bo@x.com"},
		WantAnswered: nil,
	}, { // Test 2: An asker who is not the first interned id is resolved back to
		// themselves rather than to whoever sits at index zero.
		Name:         "asker interned late",
		Asked:        "cy@x.com",
		Participants: []model.ID{"ann@x.com", "bo@x.com", "cy@x.com"},
		WantAsked:    "cy@x.com",
		WantAnswered: []model.ID{"ann@x.com", "bo@x.com"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "episodes.json")
			s := newTestStore()
			ep := testEpisode("a", 10, test.Participants...)
			ep.Asked = test.Asked
			ep.Body = "certificate renewal expired"
			s.Add(ep)
			if err := s.Save(path); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			loaded, ok := got.Episode("a")
			if !ok {
				t.Fatal("episode did not survive the round trip")
			}
			if diff := cmp.Diff(test.WantAsked, loaded.Asked); diff != "" {
				t.Errorf("Asked mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantAnswered, loaded.Answered(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Answered mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestForgetPersonClearsAsked verifies a purged person does not survive as the
// asker of an episode kept for the people who answered them. A pointer that
// still names them is the same leak as a participant list that does.
func TestForgetPersonClearsAsked(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name         string
		Asked        model.ID
		Participants []model.ID
		Purge        []model.ID
		WantKept     bool
		WantAsked    model.ID
		WantAnswered []model.ID
	}{{ // Test 0: Purging the asker leaves the episode with no asker at all.
		Name:         "asker purged",
		Asked:        "ann@x.com",
		Participants: []model.ID{"ann@x.com", "bo@x.com"},
		Purge:        []model.ID{"ann@x.com"},
		WantKept:     true,
		WantAnswered: nil,
	}, { // Test 1: Purging someone else leaves the asker untouched.
		Name:         "answerer purged",
		Asked:        "ann@x.com",
		Participants: []model.ID{"ann@x.com", "bo@x.com", "cy@x.com"},
		Purge:        []model.ID{"bo@x.com"},
		WantKept:     true,
		WantAsked:    "ann@x.com",
		WantAnswered: []model.ID{"cy@x.com"},
	}, { // Test 2: An asker the participant list never named is still purged, so
		// an episode kept for the people who answered cannot carry them.
		Name:         "asker outside the participants",
		Asked:        "dee@x.com",
		Participants: []model.ID{"ann@x.com", "bo@x.com"},
		Purge:        []model.ID{"dee@x.com"},
		WantKept:     true,
		WantAnswered: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			s := newTestStore()
			ep := testEpisode("a", 10, test.Participants...)
			ep.Asked = test.Asked
			ep.Body = "certificate renewal expired"
			s.Add(ep)
			s.ForgetPerson(test.Purge)
			got, ok := s.Episode("a")
			if ok != test.WantKept {
				t.Fatalf("episode kept = %v, want %v", ok, test.WantKept)
			}
			if !ok {
				return
			}
			if diff := cmp.Diff(test.WantAsked, got.Asked); diff != "" {
				t.Errorf("Asked mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantAnswered, got.Answered(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Answered mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestAskedSurvivesReindexWithoutArchive verifies a later run that reports the
// same conversation without the asker keeps the one already stored. A source
// that stops reporting who opened a thread has not established that nobody did.
func TestAskedSurvivesReindexWithoutArchive(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	first := testEpisode("a", 10, "ann@x.com", "bo@x.com")
	first.Asked = "ann@x.com"
	first.Archive = []Note{{Author: "bo@x.com", At: fixedNow, Text: "bump the cert"}}
	first.Body = "certificate renewal expired"
	s.Add(first)

	second := testEpisode("a", 9, "ann@x.com")
	second.Body = "certificate renewal expired"
	s.Add(second)

	got, ok := s.Episode("a")
	if !ok {
		t.Fatal("episode did not survive re-indexing")
	}
	if diff := cmp.Diff(model.ID("ann@x.com"), got.Asked); diff != "" {
		t.Errorf("Asked mismatch (-want +got):\n%s", diff)
	}
}
