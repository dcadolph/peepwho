package index

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/model"
)

// answerIndex builds two people who share a subject through their own work, so
// a test can tell what answering added from what was already there.
func answerIndex(t *testing.T) *Index {
	t.Helper()
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Ann Lee", Email: "ann@corp.com",
			AltIDs: []string{"github:ann"}, Topics: []string{"billing"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Bo Ray", Email: "bo@corp.com",
			AltIDs: []string{"github:bo"}, Topics: []string{"billing"}, Source: "git"},
	})
	ix.AutoJoin()
	ix.Canonicalize()
	return ix
}

// answerEpisode is one conversation about a named subject.
func answerEpisode(asked model.ID, participants ...model.ID) episode.Episode {
	return episode.Episode{
		ID: "gh:1", Source: "github", Kind: episode.KindChange,
		Title: "retry ledger", Asked: asked, Participants: participants,
	}
}

func TestScoreAnswers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name        string
		Eps         []episode.Episode
		WantEps     int
		WantUnknown []string
		// WantHolds names the people who gain the episode's subject.
		WantHolds []model.ID
	}{{ // Test 0: The answerer gains the subject and the asker gains nothing.
		Name:      "answerer credited, asker not",
		Eps:       []episode.Episode{answerEpisode("ann@corp.com", "ann@corp.com", "bo@corp.com")},
		WantEps:   1,
		WantHolds: []model.ID{"bo@corp.com"},
	}, { // Test 1: An episode with no asker has no answerers and credits nobody.
		Name:    "no asker",
		Eps:     []episode.Episode{answerEpisode("", "ann@corp.com", "bo@corp.com")},
		WantEps: 0,
	}, { // Test 2: An asker who drew no replies credits nobody.
		Name:    "asker alone",
		Eps:     []episode.Episode{answerEpisode("ann@corp.com", "ann@corp.com")},
		WantEps: 0,
	}, { // Test 3: Everyone who replied is credited, not only the first, and an
		// asker the graph never met is not reported as an unknown answerer.
		Name: "several answerers",
		Eps: []episode.Episode{answerEpisode("cy@corp.com",
			"cy@corp.com", "ann@corp.com", "bo@corp.com")},
		WantEps:   1,
		WantHolds: []model.ID{"ann@corp.com", "bo@corp.com"},
	}, { // Test 4: An answerer the graph never met is named, never invented.
		Name:        "unknown answerer",
		Eps:         []episode.Episode{answerEpisode("ann@corp.com", "ann@corp.com", "zed@corp.com")},
		WantEps:     1,
		WantUnknown: []string{"zed@corp.com"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			ix := answerIndex(t)
			got := ix.ScoreAnswers(test.Eps)
			if got.Episodes != test.WantEps {
				t.Errorf("Episodes = %d, want %d", got.Episodes, test.WantEps)
			}
			if diff := cmp.Diff(test.WantUnknown, got.Unknown, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unknown mismatch (-want +got):\n%s", diff)
			}
			ledger := topicID("ledger")
			var holds []model.ID
			for _, id := range []model.ID{"ann@corp.com", "bo@corp.com"} {
				p := ix.Graph.People[ix.CanonicalID(id)]
				if p == nil {
					t.Fatalf("no person for %s", id)
				}
				if p.Topics[ledger] > 0 {
					holds = append(holds, id)
				}
				// Whatever Topics gained, Direct gained with it: ownership reads
				// Direct and falls back to Topics only for somebody who has none.
				if p.Topics[ledger] != p.Direct[ledger] {
					t.Errorf("%s: Topics[ledger] = %v, Direct[ledger] = %v, want them equal",
						id, p.Topics[ledger], p.Direct[ledger])
				}
			}
			if diff := cmp.Diff(test.WantHolds, holds, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("holders of the subject mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestScoreAnswersLeavesEarnedWorkAlone checks that answering adds to a
// subject rather than replacing what the person's own work earned, and that
// the answer is worth less than the work.
func TestScoreAnswersLeavesEarnedWorkAlone(t *testing.T) {
	t.Parallel()
	ix := answerIndex(t)
	billing := topicID("billing")
	before := ix.Graph.People[ix.CanonicalID("bo@corp.com")].Topics[billing]
	if before <= 0 {
		t.Fatalf("fixture gave bo no billing weight: %v", before)
	}
	ix.ScoreAnswers([]episode.Episode{{
		ID: "gh:2", Source: "github", Kind: episode.KindChange, Title: "billing",
		Asked: "ann@corp.com", Participants: []model.ID{"ann@corp.com", "bo@corp.com"},
	}})
	after := ix.Graph.People[ix.CanonicalID("bo@corp.com")].Topics[billing]
	if after <= before {
		t.Errorf("billing weight went from %v to %v, want the answer to add", before, after)
	}
	if gained := after - before; gained >= before {
		t.Errorf("one answer added %v against %v of earned work, want an answer to weigh less",
			gained, before)
	}
}

// TestScoreAnswersIsDeterministic checks that the same episodes scored twice
// over equal indexes leave equal weights, since a byte-identical map from a
// byte-identical input is a product claim.
func TestScoreAnswersIsDeterministic(t *testing.T) {
	t.Parallel()
	eps := []episode.Episode{
		answerEpisode("ann@corp.com", "ann@corp.com", "bo@corp.com"),
		{ID: "gh:2", Source: "github", Kind: episode.KindChange, Title: "payout ledger",
			Asked: "bo@corp.com", Participants: []model.ID{"bo@corp.com", "ann@corp.com"}},
	}
	first, second := answerIndex(t), answerIndex(t)
	gotFirst := first.ScoreAnswers(eps)
	gotSecond := second.ScoreAnswers(eps)
	if diff := cmp.Diff(gotFirst, gotSecond, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("credit mismatch (-first +second):\n%s", diff)
	}
	for _, id := range []model.ID{"ann@corp.com", "bo@corp.com"} {
		a := first.Graph.People[first.CanonicalID(id)]
		b := second.Graph.People[second.CanonicalID(id)]
		if diff := cmp.Diff(a.Topics, b.Topics, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("%s topics mismatch (-first +second):\n%s", id, diff)
		}
		if diff := cmp.Diff(a.Direct, b.Direct, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("%s direct mismatch (-first +second):\n%s", id, diff)
		}
	}
}
