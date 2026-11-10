package index

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/model"
)

// teamFixturePeople returns the person records of the teams fixture: two
// people who own contracts and benefits, and one who only mentioned contracts.
func teamFixturePeople() []connector.Record {
	return []connector.Record{
		{Kind: connector.KindPerson, Name: "Carla Counsel", Email: "carla@corp.com",
			Topics: []string{"contracts", "legal-review"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Ben Fitz", Email: "ben@corp.com",
			Topics: []string{"benefits"}, Source: "jira"},
		{Kind: connector.KindPerson, Name: "Pat Passer", Email: "pat@corp.com",
			Text: "will read the contracts later", Source: "slack"},
	}
}

// teamFixtureTeams returns the CODEOWNERS team records of the teams fixture:
// a squad whose handle names legal and a squad declared only on a catch-all.
func teamFixtureTeams() []connector.Record {
	return []connector.Record{
		{Kind: connector.KindPerson, Source: "codeowners", Weight: 1, Name: "@acme/legal-squad",
			PersonID: "codeowners:acme/legal-squad", Topics: []string{"contracts"}},
		{Kind: connector.KindPerson, Source: "codeowners", Weight: 1, Name: "@acme/default-maintainers",
			PersonID: "codeowners:acme/default-maintainers"},
	}
}

// teamFixture builds an index from the fixture people, with the team records
// added when withTeams is set.
func teamFixture(t *testing.T, withTeams bool) *Index {
	t.Helper()
	recs := teamFixturePeople()
	if withTeams {
		recs = append(recs, teamFixtureTeams()...)
	}
	ix := New()
	ix.Build(recs)
	ix.Canonicalize()
	return ix
}

// TestHandleWords verifies a team's own words come from the part of its handle
// after the organization.
func TestHandleWords(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantWords []string
		In        model.ID
	}{{ // Test 0: Organization and source prefix are dropped, hyphens split.
		In: "codeowners:simcorp/legal-squad", WantWords: []string{"legal", "squad"},
	}, { // Test 1: Underscores split too.
		In: "codeowners:acme/default_maintainers", WantWords: []string{"default", "maintainers"},
	}, { // Test 2: A nested team keeps only its last segment.
		In: "codeowners:acme/infra/sre-oncall", WantWords: []string{"sre", "oncall"},
	}, { // Test 3: No prefix and no organization.
		In: "payments", WantWords: []string{"payments"},
	}, { // Test 4: Empty handle.
		In: "", WantWords: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := handleWords(test.In)
			if diff := cmp.Diff(test.WantWords, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("handleWords(%q) mismatch (-want +got):\n%s", test.In, diff)
			}
		})
	}
}

// TestSearchTeams verifies teams are found by the subjects they own and by the
// words of their handles, and that an index without teams finds none.
func TestSearchTeams(t *testing.T) {
	t.Parallel()
	withTeams, withoutTeams := teamFixture(t, true), teamFixture(t, false)
	tests := []struct {
		Index       *Index
		Query       string
		WantIDs     []model.ID
		WantReasons []string
	}{{ // Test 0: A subject the team owns.
		Index: withTeams, Query: "contracts",
		WantIDs:     []model.ID{"codeowners:acme/legal-squad"},
		WantReasons: []string{"contracts (topic)", `legal (name) for "contracts"`},
	}, { // Test 1: A word of the handle the team declares no path for.
		Index: withTeams, Query: "legal",
		WantIDs:     []model.ID{"codeowners:acme/legal-squad"},
		WantReasons: []string{`contracts (topic) for "legal"`, "legal (name)"},
	}, { // Test 2: A catch-all team is found by its handle alone.
		Index: withTeams, Query: "maintainers",
		WantIDs:     []model.ID{"codeowners:acme/default-maintainers"},
		WantReasons: []string{"maintainers (name)"},
	}, { // Test 3: A subject no team owns.
		Index: withTeams, Query: "benefits",
	}, { // Test 4: No teams in the index.
		Index: withoutTeams, Query: "contracts",
	}, { // Test 5: A query with no searchable terms.
		Index: withTeams, Query: "the",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := test.Index.SearchTeams(test.Query, 0)
			var ids []model.ID
			for _, m := range got {
				ids = append(ids, m.Person.ID)
				if !m.Declared {
					t.Errorf("team %s is not marked declared", m.Person.ID)
				}
			}
			if diff := cmp.Diff(test.WantIDs, ids, cmpopts.EquateEmpty()); diff != "" {
				t.Fatalf("SearchTeams(%q) ids mismatch (-want +got):\n%s", test.Query, diff)
			}
			if len(got) > 0 {
				if diff := cmp.Diff(test.WantReasons, got[0].Reasons); diff != "" {
					t.Errorf("SearchTeams(%q) reasons mismatch (-want +got):\n%s", test.Query, diff)
				}
			}
		})
	}
}

// TestSearchTeamsLimit verifies a positive limit caps the teams returned.
func TestSearchTeamsLimit(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Source: "codeowners", Name: "@acme/a-squad",
			PersonID: "codeowners:acme/a-squad", Topics: []string{"contracts"}},
		{Kind: connector.KindPerson, Source: "codeowners", Name: "@acme/b-squad",
			PersonID: "codeowners:acme/b-squad", Topics: []string{"contracts"}},
	})
	tests := []struct {
		Limit     int
		WantCount int
	}{{ // Test 0: Zero returns all.
		Limit: 0, WantCount: 2,
	}, { // Test 1: One caps at one.
		Limit: 1, WantCount: 1,
	}, { // Test 2: A limit above the count returns all.
		Limit: 5, WantCount: 2,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := len(ix.SearchTeams("contracts", test.Limit)); got != test.WantCount {
				t.Errorf("SearchTeams limit %d = %d teams, want %d", test.Limit, got, test.WantCount)
			}
		})
	}
}

// matchKey is the part of a people match the equality check compares.
type matchKey struct {
	// ID is the matched person.
	ID model.ID
	// Score is the ranking score.
	Score float64
	// Strength is the match strength.
	Strength float64
}

// matchKeys reduces matches to what ranking decides, in order.
func matchKeys(ms []model.Match) []matchKey {
	out := make([]matchKey, 0, len(ms))
	for _, m := range ms {
		out = append(out, matchKey{ID: m.Person.ID, Score: m.Score, Strength: m.Strength})
	}
	return out
}

// TestSearchIgnoresTeams verifies the people ranking is exactly what an index
// without the team records gives, and that no team ever appears in it, in
// Search, SemanticPeople's field, Near, and TopicExperts.
func TestSearchIgnoresTeams(t *testing.T) {
	t.Parallel()
	withTeams, withoutTeams := teamFixture(t, true), teamFixture(t, false)
	tests := []struct {
		Query string
		Limit int
	}{{ // Test 0: A subject a team owns.
		Query: "contracts", Limit: 0,
	}, { // Test 1: The same under a cut.
		Query: "contracts", Limit: 1,
	}, { // Test 2: A handle word.
		Query: "legal", Limit: 0,
	}, { // Test 3: A subject no team owns.
		Query: "benefits", Limit: 0,
	}, { // Test 4: A catch-all handle word matches nobody.
		Query: "maintainers", Limit: 0,
	}, { // Test 5: Two subjects.
		Query: "contracts benefits", Limit: 2,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := withTeams.Search(test.Query, test.Limit)
			want := withoutTeams.Search(test.Query, test.Limit)
			if diff := cmp.Diff(matchKeys(want), matchKeys(got), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Search(%q, %d) moved with teams present (-without +with):\n%s",
					test.Query, test.Limit, diff)
			}
			for _, m := range got {
				if m.Person.ID.IsTeam() {
					t.Errorf("Search(%q) returned team %s", test.Query, m.Person.ID)
				}
			}
		})
	}

	team := model.ID("codeowners:acme/legal-squad")
	if got := withTeams.Near(team, 5); got != nil {
		t.Errorf("Near(team) = %v, want nil", got)
	}
	for _, a := range withTeams.Near("carla@corp.com", 0) {
		if a.ID.IsTeam() {
			t.Errorf("Near listed team %s", a.ID)
		}
	}
	for tid := range withTeams.Graph.Topics {
		for _, p := range withTeams.TopicExperts(tid, 0) {
			if p.ID.IsTeam() {
				t.Errorf("TopicExperts(%s) listed team %s", tid, p.ID)
			}
		}
	}
}
