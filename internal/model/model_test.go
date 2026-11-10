package model

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestNewGraph verifies the constructor returns usable, non-nil maps.
func TestNewGraph(t *testing.T) {
	t.Parallel()
	g := NewGraph()
	if g.People == nil || g.Teams == nil || g.Orgs == nil || g.Topics == nil {
		t.Fatal("NewGraph returned nil maps")
	}

	g.People["a@x.com"] = &Person{ID: "a@x.com", Name: "A", Topics: map[ID]float64{"go": 1}}
	g.Teams["core"] = &Team{ID: "core", Name: "Core"}

	want := &Person{ID: "a@x.com", Name: "A", Topics: map[ID]float64{"go": 1}}
	if diff := cmp.Diff(want, g.People["a@x.com"], cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("person mismatch (-want +got):\n%s", diff)
	}
	if len(g.Teams) != 1 {
		t.Errorf("team count = %d, want 1", len(g.Teams))
	}
}

// TestSalientKeepsProseOutOfSubjects checks a single ordinary word mined from
// text never becomes a subject, however many sources it turns up in, while a
// stated subject and a mined phrase both do.
//
// Appearing in two sources sounds like corroboration and is not: the commonest
// words appear everywhere. Measured on a real tracker indexed from two sources,
// the old rule promoted 7,528 mined words to subjects and they were "appearing",
// "avoiding", "overkill", "secondly", and "work". Those reached the risk report,
// ownership, and the connections between subjects.
func TestSalientKeepsProseOutOfSubjects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name    string
		Topic   Topic
		WantYes bool
	}{{ // Test 0: A source stated it, so it is a subject however it is spelled.
		Name:  "stated single word",
		Topic: Topic{ID: "billing", Curated: true, Sources: []string{"jira"}}, WantYes: true,
	}, { // Test 1: One ordinary word out of prose, seen twice. Vocabulary.
		Name:  "mined word in two sources",
		Topic: Topic{ID: "overkill", Sources: []string{"git", "confluence"}}, WantYes: false,
	}, { // Test 2: A mined phrase names something, so it earns its place.
		Name:  "mined phrase in two sources",
		Topic: Topic{ID: "state-store", Sources: []string{"git", "confluence"}}, WantYes: true,
	}, { // Test 3: A phrase from one source alone has nothing corroborating it.
		Name:  "mined phrase in one source",
		Topic: Topic{ID: "state-store", Sources: []string{"git"}}, WantYes: false,
	}, { // Test 4: A phrase of grammar names nothing, however many sources saw it.
		Name:  "mined grammar phrase",
		Topic: Topic{ID: "should-have", Sources: []string{"git", "confluence"}}, WantYes: false,
	}, { // Test 5: A ticket reference is not an area of work.
		Name:  "mined ticket reference",
		Topic: Topic{ID: "kip-1076", Sources: []string{"jira", "confluence"}}, WantYes: false,
	}, { // Test 6: What everybody holds distinguishes nobody, stated or not.
		Name:  "ubiquitous",
		Topic: Topic{ID: "billing", Curated: true, Ubiquitous: true}, WantYes: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			if got := test.Topic.Salient(); got != test.WantYes {
				t.Errorf("Salient() = %v, want %v for %s", got, test.WantYes, test.Name)
			}
		})
	}
}

// TestPersonReach verifies Reach sums every topic weight and gives the same
// bits however the map was built. The weights in test 2 sum to a different
// value in each order, so a sum taken in map order fails it.
func TestPersonReach(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Topics    map[ID]float64
		WantReach float64
	}{{ // Test 0: No topics.
		Topics: nil, WantReach: 0,
	}, { // Test 1: Exact weights.
		Topics: map[ID]float64{"go": 1, "k8s": 2.5, "sql": 0.5}, WantReach: 4,
	}, { // Test 2: Weights whose sum depends on order, taken in sorted topic order.
		Topics: map[ID]float64{"a": 1e16, "b": 1, "c": -1e16, "d": 1, "e": 0.1, "f": 0.2, "g": 0.3},
		// In sorted order 1e16+1 rounds the 1 away, so only the second 1 survives.
		WantReach: 1.6,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			for range 200 {
				topics := make(map[ID]float64, len(test.Topics))
				for k, v := range test.Topics {
					topics[k] = v
				}
				if got := (&Person{Topics: topics}).Reach(); got != test.WantReach {
					t.Fatalf("Reach = %v, want %v", got, test.WantReach)
				}
			}
		})
	}
}

// TestIDIsTeam verifies a CODEOWNERS team id is told apart from a person id.
func TestIDIsTeam(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ID       ID
		WantTeam bool
	}{{ // Test 0: A CODEOWNERS team.
		ID: "codeowners:acme/ledger-squad", WantTeam: true,
	}, { // Test 1: A CODEOWNERS user handle.
		ID: "codeowners:zoe", WantTeam: false,
	}, { // Test 2: An email id.
		ID: "zoe@x.com", WantTeam: false,
	}, { // Test 3: A source id.
		ID: "slack:U123", WantTeam: false,
	}, { // Test 4: Empty.
		ID: "", WantTeam: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := test.ID.IsTeam(); got != test.WantTeam {
				t.Errorf("IsTeam(%q) = %v, want %v", test.ID, got, test.WantTeam)
			}
		})
	}
}

// TestPeopleCount verifies teams kept as graph people are not counted.
func TestPeopleCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		People    map[ID]*Person
		WantCount int
	}{{ // Test 0: Nil map.
		People: nil, WantCount: 0,
	}, { // Test 1: People and one team.
		People: map[ID]*Person{
			"zoe@x.com": {}, "codeowners:zoe": {}, "codeowners:acme/ledger-squad": {},
		},
		WantCount: 2,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := PeopleCount(test.People); got != test.WantCount {
				t.Errorf("PeopleCount = %d, want %d", got, test.WantCount)
			}
		})
	}
}
