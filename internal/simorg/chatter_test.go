package simorg

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// shownMatch is the part of a person or channel match a reader sees.
type shownMatch struct {
	// ID names the person or channel.
	ID model.ID
	// Score is the ranking score.
	Score float64
	// Strength is the shown strength.
	Strength float64
	// Reasons are the shown reason lines.
	Reasons []string
}

// shownPeople projects person matches to what a reader sees.
func shownPeople(ms []model.Match) []shownMatch {
	out := make([]shownMatch, 0, len(ms))
	for _, m := range ms {
		out = append(out, shownMatch{ID: m.Person.ID, Score: m.Score, Strength: m.Strength, Reasons: m.Reasons})
	}
	return out
}

// shownChannels projects channel matches to what a reader sees.
func shownChannels(ms []model.ChannelMatch) []shownMatch {
	out := make([]shownMatch, 0, len(ms))
	for _, m := range ms {
		out = append(out, shownMatch{ID: m.Channel.ID, Score: m.Score, Strength: m.Strength, Reasons: m.Reasons})
	}
	return out
}

// TestResolvePrefixConsistent checks that a smaller answer is the head of a
// larger one: for every limit n below m, the people, departed, and channels at
// n are the first n of those at m. Filtering after the cut broke this, since
// chatter inside a small cut left the answer short of a match a larger cut
// kept.
func TestResolvePrefixConsistent(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	res := resolve.NewKeyword(ix)
	tests := []struct {
		Query string
	}{
		{Query: "kafka lag"}, // Test 0: Tie reach behind a direct owner.
		{Query: "contracts"}, // Test 1: A mention inside the old cut.
		{Query: "legal"},     // Test 2: A team name.
		{Query: "benefits"},  // Test 3: A team name with a subject.
		{Query: "billing"},   // Test 4: A bare word.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			for _, m := range []int{2, 5, 10, index.EvidenceWindow} {
				big, err := res.Resolve(context.Background(), test.Query, m)
				if err != nil {
					t.Fatalf("Resolve(%q, %d): %v", test.Query, m, err)
				}
				for n := 1; n < m; n++ {
					small, err := res.Resolve(context.Background(), test.Query, n)
					if err != nil {
						t.Fatalf("Resolve(%q, %d): %v", test.Query, n, err)
					}
					head := func(s []shownMatch) []shownMatch { return s[:min(n, len(s))] }
					checks := []struct {
						List      string
						WantShown []shownMatch
						Got       []shownMatch
					}{
						{"people", head(shownPeople(big.People)), shownPeople(small.People)},
						{"departed", head(shownPeople(big.Departed)), shownPeople(small.Departed)},
						{"channels", head(shownChannels(big.Channels)), shownChannels(small.Channels)},
					}
					for _, c := range checks {
						if diff := cmp.Diff(c.WantShown, c.Got, cmpopts.EquateEmpty()); diff != "" {
							t.Errorf("%q %s at %d is not the head of %d (-want +got):\n%s",
								test.Query, c.List, n, m, diff)
						}
					}
				}
			}
		})
	}
}

// TestAnswerDropsTieReach checks that "kafka lag", whose owner states both
// words, leaves out the people and channels that only know a subject traveling
// with it, although the search reaches them and they rest on a stated topic.
// Filling the limit after the filter brought exactly these back once.
func TestAnswerDropsTieReach(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	const query = "kafka lag"
	people, _ := ix.SearchSplit(query, index.EvidenceWindow)
	var reach []model.ID
	for _, m := range people {
		if m.Declared && m.Reach {
			reach = append(reach, m.Person.ID)
		}
	}
	if len(reach) == 0 {
		t.Fatalf("%q reaches nobody through ties, so this test proves nothing", query)
	}
	var channelReach []model.ID
	for _, m := range ix.SearchChannels(query, index.EvidenceWindow) {
		if m.Declared && m.Reach {
			channelReach = append(channelReach, m.Channel.ID)
		}
	}
	if len(channelReach) == 0 {
		t.Fatalf("%q reaches no channel through ties, so this test proves nothing", query)
	}
	ans, err := resolve.NewKeyword(ix).Resolve(context.Background(), query, 5)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", query, err)
	}
	for _, m := range slices.Concat(ans.People, ans.Departed) {
		if m.Reach {
			t.Errorf("%q: %s rides along on tie reach behind a direct owner", query, m.Person.ID)
		}
	}
	for _, m := range ans.Channels {
		if m.Reach {
			t.Errorf("%q: channel %s rides along on tie reach behind a direct owner", query, m.Channel.Name)
		}
	}
	if len(ans.People) == 0 || ans.People[0].Person.ID != "stanley.lewis7@example.com" {
		t.Errorf("%q: leader is not Stanley Lewis: %v", query, shownPeople(ans.People))
	}
}

// TestAnswerFillsPastChatter checks that a mention inside the first cut no
// longer costs the answer a stated owner just below it: "contracts" at limit 2
// names Carlton Halpert and then Phyllis Darmody.
func TestAnswerFillsPastChatter(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	ans, err := resolve.NewKeyword(ix).Resolve(context.Background(), "contracts", 2)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := make([]string, 0, len(ans.People))
	for _, m := range ans.People {
		got = append(got, m.Person.Name)
	}
	if diff := cmp.Diff([]string{"Carlton Halpert", "Phyllis Darmody"}, got); diff != "" {
		t.Errorf("contracts at limit 2 (-want +got):\n%s", diff)
	}
}

// TestAnswerDropsChatter asks the demo company about every subject by name and
// checks that an answer led by a declared match carries nobody and no channel
// that only mentioned the words, which is what padded "kafka lag" out to five
// with a recruiter and a benefits channel.
func TestAnswerDropsChatter(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	res := resolve.NewKeyword(ix)
	trimmed := 0
	for _, s := range subjects {
		ans, err := res.Resolve(context.Background(), s.Topic, 5)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", s.Topic, err)
		}
		if len(ans.People) > 0 && ans.People[0].Declared {
			for _, m := range ans.People {
				if !m.Declared {
					t.Errorf("%q: undeclared %s rode along behind a declared leader", s.Topic, m.Person.ID)
				}
			}
		}
		if len(ans.Channels) > 0 && ans.Channels[0].Declared {
			for _, m := range ans.Channels {
				if !m.Declared {
					t.Errorf("%q: undeclared channel %s rode along behind a declared leader",
						s.Topic, m.Channel.Name)
				}
			}
		}
		if len(ans.People) < len(ix.Search(s.Topic, 5)) {
			trimmed++
		}
	}
	if trimmed == 0 {
		t.Error("no answer was trimmed, so this test proves nothing about trimming")
	}
}

// TestAnswerKeepsOwner asks every question in the simulated company and checks
// that trimming never costs the right person: whenever the untrimmed ranking
// held the owner, the answer still does. A common word such as "people" hits a
// whole team by name, and treating that as declared once dropped the login
// owner who led on what they had said.
func TestAnswerKeepsOwner(t *testing.T) {
	t.Parallel()
	built, err := Build(Spec{Seed: 7}, t.TempDir())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const limit = 5
	res := resolve.NewKeyword(built.Index)
	for _, q := range built.Org.Questions {
		if q.WantPerson == "" {
			continue
		}
		ans, err := res.Resolve(context.Background(), q.Text, limit)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", q.Text, err)
		}
		held := false
		for _, m := range built.Index.Search(q.Text, limit) {
			held = held || m.Person.ID == q.WantPerson
		}
		kept := false
		for _, m := range ans.People {
			kept = kept || m.Person.ID == q.WantPerson
		}
		if held && !kept {
			t.Errorf("%q: owner %s was in the untrimmed ranking and is gone from the answer",
				q.Text, q.WantPerson)
		}
	}
}
