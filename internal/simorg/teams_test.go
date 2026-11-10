package simorg

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// bigSourceOrder is the order BuildBigIndex reads the demo company's sources in.
var bigSourceOrder = []string{ //nolint:gochecknoglobals // Not modified, simplifies testing.
	"org-csv", "codeowners", "slack", "github", "jira", "confluence", "pagerduty", "git",
}

// withoutTeamRecords rebuilds ix from its own source records with every
// CODEOWNERS team record removed, the way BuildBigIndex builds it.
func withoutTeamRecords(t *testing.T, ix *index.Index) *index.Index {
	t.Helper()
	if diff := cmp.Diff(len(bigSourceOrder), len(ix.SourceNames())); diff != "" {
		t.Fatalf("source count mismatch (-want +got):\n%s", diff)
	}
	out := index.New()
	dropped := 0
	for _, name := range bigSourceOrder {
		var keep []connector.Record
		for _, rec := range ix.SourceRecords(name) {
			if rec.Kind == connector.KindPerson && model.ID(rec.PersonID).IsTeam() {
				dropped++
				continue
			}
			keep = append(keep, rec)
		}
		out.Add(keep)
	}
	if dropped == 0 {
		t.Fatal("the demo company holds no team records, so the comparison proves nothing")
	}
	out.AutoJoin()
	out.Canonicalize()
	return out
}

// peopleKey is the part of a people match the equality check compares.
type peopleKey struct {
	// ID is the matched person.
	ID model.ID
	// Score is the ranking score.
	Score float64
	// Strength is the match strength.
	Strength float64
}

// peopleKeys reduces an answer's people to what ranking decides, in order.
func peopleKeys(ms []model.Match) []peopleKey {
	out := make([]peopleKey, 0, len(ms))
	for _, m := range ms {
		out = append(out, peopleKey{ID: m.Person.ID, Score: m.Score, Strength: m.Strength})
	}
	return out
}

// TestTeamsOfRecordOnTheSample asks the demo company about subjects its
// CODEOWNERS squads own and checks the squads answer in their own section,
// labeled with their org chart team, and never among the people.
func TestTeamsOfRecordOnTheSample(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	k := resolve.NewKeyword(ix)
	tests := []struct {
		Query         string
		WantPeople    []string
		WantFirstTeam string
		WantTeamLabel string
		Limit         int
	}{{ // Test 0: The legal squad answers legal beside its people.
		Query: "legal", Limit: 5,
		WantFirstTeam: "codeowners:simcorp/legal-squad", WantTeamLabel: "Legal",
	}, { // Test 1: The people squad answers benefits.
		Query: "benefits", Limit: 5,
		WantFirstTeam: "codeowners:simcorp/people-squad", WantTeamLabel: "People",
	}, { // Test 2: The squad no longer takes Phyllis's place under the cut.
		Query: "contracts", Limit: 3,
		WantPeople:    []string{"Carlton Halpert", "Phyllis Darmody"},
		WantFirstTeam: "codeowners:simcorp/legal-squad", WantTeamLabel: "Legal",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ans, err := k.Resolve(context.Background(), test.Query, test.Limit)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", test.Query, err)
			}
			var names []string
			for _, m := range ans.People {
				if m.Person.ID.IsTeam() {
					t.Errorf("Resolve(%q) listed team %s among the people", test.Query, m.Person.ID)
				}
				names = append(names, m.Person.Name)
			}
			if test.WantPeople != nil {
				if diff := cmp.Diff(test.WantPeople, names); diff != "" {
					t.Errorf("Resolve(%q) people mismatch (-want +got):\n%s", test.Query, diff)
				}
			}
			if len(ans.Teams) == 0 {
				t.Fatalf("Resolve(%q) named no team of record", test.Query)
			}
			first := ans.Teams[0]
			if diff := cmp.Diff(test.WantFirstTeam, string(first.Person.ID)); diff != "" {
				t.Errorf("Resolve(%q) first team mismatch (-want +got):\n%s", test.Query, diff)
			}
			label := ""
			if first.Team != nil {
				label = first.Team.Name
			}
			if diff := cmp.Diff(test.WantTeamLabel, label); diff != "" {
				t.Errorf("Resolve(%q) team label mismatch (-want +got):\n%s", test.Query, diff)
			}
		})
	}

	t.Run("search", func(t *testing.T) {
		t.Parallel()
		got := resolve.Search(ix, "legal-squad", 5)
		if len(got) == 0 {
			t.Fatal("Search(legal-squad) found nothing")
		}
		if diff := cmp.Diff("team", got[0].Kind); diff != "" {
			t.Errorf("Search(legal-squad) kind mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("risk", func(t *testing.T) {
		t.Parallel()
		for _, r := range resolve.Risk(ix, 0) {
			if model.ID(r.LeadID).IsTeam() {
				t.Errorf("risk on %s leads with team %s", r.Topic, r.LeadID)
			}
			for _, e := range r.Experts {
				if model.ID(e.ID).IsTeam() {
					t.Errorf("risk on %s counts team %s as an expert", r.Topic, e.ID)
				}
			}
		}
	})
}

// TestTeamsLeavePeopleRankingUnchanged checks that, for every subject in the
// demo company at a cut and with none, the people an answer lists are exactly
// those an index built without the team records lists, in score, strength,
// and order.
func TestTeamsLeavePeopleRankingUnchanged(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	with, without := resolve.NewKeyword(ix), resolve.NewKeyword(withoutTeamRecords(t, ix))
	queries := []string{"legal", "benefits", "contracts", "who owns contracts", "kafka lag"}
	for id := range ix.Graph.Topics {
		queries = append(queries, string(id))
	}
	for _, limit := range []int{5, 0} {
		for _, q := range queries {
			got, err := with.Resolve(context.Background(), q, limit)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", q, err)
			}
			want, err := without.Resolve(context.Background(), q, limit)
			if err != nil {
				t.Fatalf("Resolve(%q) without teams: %v", q, err)
			}
			if diff := cmp.Diff(peopleKeys(want.People), peopleKeys(got.People), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Resolve(%q, %d) people moved with teams present (-without +with):\n%s", q, limit, diff)
			}
		}
	}
}

// stubChat is a Chatter that returns one canned reply.
type stubChat struct {
	// reply is what Chat returns.
	reply string
}

// Chat returns the canned reply.
func (s stubChat) Chat(context.Context, string, string) (string, error) { return s.reply, nil }

// TestLLMSummaryNeverNamesATeam checks the written summary always tells the
// asker to talk to a person, whether the model reply is unusable or names a
// team handle first.
func TestLLMSummaryNeverNamesATeam(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	tests := []struct {
		Reply string
	}{{ // Test 0: An unusable reply falls back to keyword order.
		Reply: "not json",
	}, { // Test 1: A reply that puts the legal squad first.
		Reply: `{"people":["codeowners:simcorp/legal-squad","@simcorp/legal-squad"]}`,
	}}
	queries := []string{"legal", "benefits", "contracts"}
	for _, s := range subjects {
		queries = append(queries, s.Topic)
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			llm := resolve.NewLLM(ix, stubChat{reply: test.Reply}, nil)
			for _, q := range queries {
				ans, err := llm.Resolve(context.Background(), q, 3)
				if err != nil {
					t.Fatalf("Resolve(%q): %v", q, err)
				}
				if strings.HasPrefix(ans.Summary, "Talk to @") {
					t.Errorf("Resolve(%q) summary names a team: %q", q, ans.Summary)
				}
				for _, m := range ans.People {
					if m.Person.ID.IsTeam() {
						t.Errorf("Resolve(%q) listed team %s among the people", q, m.Person.ID)
					}
				}
			}
		})
	}
}
