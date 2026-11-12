package resolve

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// searchFixture builds a small index across people and a channel for the search
// tests: two Payments engineers who know billing, a designer, and a channel.
func searchFixture() *index.Index {
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Kevin Novak", Email: "kevin@corp.com", Title: "Software Engineer", Team: "Payments", Topics: []string{"retries", "billing"}, Source: "t"},
		{Kind: connector.KindPerson, Name: "Angela Malone", Email: "angela@corp.com", Title: "Staff Engineer", Team: "Payments", Topics: []string{"billing"}, Source: "t"},
		{Kind: connector.KindPerson, Name: "Dana Reed", Email: "dana@corp.com", Title: "Designer", Team: "Design", Source: "t"},
		{Kind: connector.KindChannel, Name: "payments-help", Title: "billing questions", Source: "t"},
	})
	ix.Canonicalize()
	return ix
}

// idSet lists the result ids for presence checks.
func idSet(rs []SearchResult) map[string]bool {
	out := make(map[string]bool, len(rs))
	for _, r := range rs {
		out[r.ID] = true
	}
	return out
}

// TestSearch checks name, topic, team, and channel matches, ranking, empties,
// and the limit.
func TestSearch(t *testing.T) {
	t.Parallel()
	ix := searchFixture()

	// A name query ranks the person first, matched on name.
	kev := Search(ix, "kevin", 0)
	if len(kev) != 1 || kev[0].ID != "kevin@corp.com" || !slices.Contains(kev[0].Matched, "name") {
		t.Fatalf("search kevin = %+v, want the person matched on name", kev)
	}

	// A topic query finds people by topic and the channel by its topic.
	bill := idSet(Search(ix, "billing", 0))
	for _, want := range []string{"kevin@corp.com", "angela@corp.com", "payments-help"} {
		if !bill[want] {
			t.Errorf("search billing missing %s: %v", want, bill)
		}
	}

	// A team query finds the two Payments people and the channel by its name.
	if pay := Search(ix, "payments", 0); len(pay) != 3 {
		t.Errorf("search payments = %d results, want 3", len(pay))
	}

	// No match is empty, and the limit caps results.
	if r := Search(ix, "zzzz", 0); len(r) != 0 {
		t.Errorf("search zzzz = %+v, want empty", r)
	}
	if r := Search(ix, "e", 1); len(r) > 1 {
		t.Errorf("limit not applied: %d results", len(r))
	}
}

// TestProfileViewJoins checks the profile view carries the inferred identity
// merges with their strength.
func TestProfileViewJoins(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Kevin Novak", Email: "kevin@corp.com", Source: "t"},
		{Kind: connector.KindPerson, PersonID: "github:kevinnovak", Name: "@kevinnovak", Source: "github"},
	})
	ix.AutoJoin()
	ix.Canonicalize()
	profile, ok := ix.Profile("kevin@corp.com")
	if !ok {
		t.Fatal("profile not found")
	}
	view := ProfileView(ix, profile)
	if len(view.Joins) != 1 || view.Joins[0].Alias != "github:kevinnovak" || view.Joins[0].Confidence != 0.9 {
		t.Errorf("view.Joins = %+v, want the github join at 0.9", view.Joins)
	}
}

// TestSortSearchResultsPutsTheActiveFirst checks a departed person never sits
// above an active person with the same kind of match, while a departed person
// found by a stronger kind of match still leads. Before this, search ordered
// by score alone, so a departed colleague who had done more work on a subject
// came back first, unmarked, for somebody to message.
func TestSortSearchResultsPutsTheActiveFirst(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In      []SearchResult
		WantIDs []string
	}{{ // Test 0: Two exact-name matches, the active one first.
		In: []SearchResult{
			{Kind: "person", ID: "a-dinesh", Name: "Dinesh White", Score: 100, Deactivated: true},
			{Kind: "person", ID: "b-dinesh", Name: "Dinesh White", Score: 100},
		},
		WantIDs: []string{"b-dinesh", "a-dinesh"},
	}, { // Test 1: A departed exact-name match stays above an active prefix match.
		In: []SearchResult{
			{Kind: "person", ID: "prefix", Name: "Dinesh Whitfield", Score: 60},
			{Kind: "person", ID: "gone", Name: "Dinesh", Score: 100, Deactivated: true},
		},
		WantIDs: []string{"gone", "prefix"},
	}, { // Test 2: A departed subject match with the higher score follows an active one.
		In: []SearchResult{
			{Kind: "person", ID: "gone", Name: "Ann", Score: 10.5, Deactivated: true},
			{Kind: "person", ID: "here", Name: "Bo", Score: 8.4},
		},
		WantIDs: []string{"here", "gone"},
	}, { // Test 3: A departed subject match falls below a channel's subject match.
		In: []SearchResult{
			{Kind: "person", ID: "gone", Name: "Ann", Score: 10.5, Deactivated: true},
			{Kind: "channel", ID: "kafka", Name: "kafka", Score: 10},
		},
		WantIDs: []string{"kafka", "gone"},
	}, { // Test 4: Two departed subject matches keep their score order.
		In: []SearchResult{
			{Kind: "person", ID: "low", Name: "Ann", Score: 8.5, Deactivated: true},
			{Kind: "person", ID: "high", Name: "Bo", Score: 10.2, Deactivated: true},
		},
		WantIDs: []string{"high", "low"},
	}, { // Test 5: A departed title match stays above an active subject match.
		In: []SearchResult{
			{Kind: "person", ID: "here", Name: "Ann", Score: 10.9},
			{Kind: "person", ID: "gone", Name: "Bo", Score: 15, Deactivated: true},
		},
		WantIDs: []string{"gone", "here"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := slices.Clone(test.In)
			SortSearchResults(got)
			if diff := cmp.Diff(test.WantIDs, resultIDs(got)); diff != "" {
				t.Errorf("order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSortSearchResultsWithNobodyDeactivated checks an index where nobody is
// deactivated keeps plain score order, with ties broken by name and then id.
func TestSortSearchResultsWithNobodyDeactivated(t *testing.T) {
	t.Parallel()
	got := []SearchResult{
		{Kind: "person", ID: "c", Name: "Cy", Score: 8.4},
		{Kind: "channel", ID: "kafka", Name: "kafka", Score: 10},
		{Kind: "person", ID: "b2", Name: "Bo", Score: 60},
		{Kind: "person", ID: "a", Name: "Al", Score: 10.5},
		{Kind: "person", ID: "b1", Name: "Bo", Score: 60},
		{Kind: "team", ID: "corp/kafka", Name: "corp/kafka", Score: 8.4},
		{Kind: "person", ID: "d", Name: "Di", Score: 9.1},
	}
	want := slices.Clone(got)
	slices.SortFunc(want, func(a, b SearchResult) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.ID, b.ID)
	})
	SortSearchResults(got)
	if diff := cmp.Diff(resultIDs(want), resultIDs(got)); diff != "" {
		t.Errorf("order mismatch (-plain score order +got):\n%s", diff)
	}
}

// TestSearchMarksTheDeactivated checks search sets the flag on a departed
// person, writes the JSON key only for them, and orders an active namesake
// first.
func TestSearchMarksTheDeactivated(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Dinesh White", Email: "a-dinesh@corp.com",
			Account: model.AccountDeactivated, Source: "slack"},
		{Kind: connector.KindPerson, Name: "Dinesh White", Email: "b-dinesh@corp.com",
			Account: model.AccountActive, Source: "slack"},
	})
	ix.Canonicalize()

	got := Search(ix, "dinesh white", 0)
	wantIDs := []string{"b-dinesh@corp.com", "a-dinesh@corp.com"}
	if diff := cmp.Diff(wantIDs, resultIDs(got)); diff != "" {
		t.Fatalf("order mismatch (-want +got):\n%s", diff)
	}
	tests := []struct {
		Result          SearchResult
		WantDeactivated bool
	}{{ // Test 0: The active namesake carries no flag and no key.
		Result: got[0], WantDeactivated: false,
	}, { // Test 1: The departed one carries the flag and the key.
		Result: got[1], WantDeactivated: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if test.Result.Deactivated != test.WantDeactivated {
				t.Errorf("deactivated = %v, want %v", test.Result.Deactivated, test.WantDeactivated)
			}
			raw, err := json.Marshal(test.Result)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			keyed := strings.Contains(string(raw), `"deactivated":true`)
			if keyed != test.WantDeactivated {
				t.Errorf("JSON %s has the key: %v, want %v", raw, keyed, test.WantDeactivated)
			}
			if !test.WantDeactivated && strings.Contains(string(raw), `"deactivated"`) {
				t.Errorf("JSON %s names deactivated for an active person", raw)
			}
		})
	}
}

// resultIDs lists the result ids in order.
func resultIDs(rs []SearchResult) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

// TestSearchChannelByTopicTag checks a channel matches on a derived topic tag,
// not only its stated topic, so channel and people topic search stay consistent.
func TestSearchChannelByTopicTag(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindChannel, Name: "eng-help", Topics: []string{"kafka"}, Source: "t"},
	})
	ix.Canonicalize()
	found := false
	for _, r := range Search(ix, "kafka", 0) {
		if r.Kind == "channel" && r.ID == "eng-help" {
			found = true
		}
	}
	if !found {
		t.Errorf("channel not found by topic tag; results: %+v", Search(ix, "kafka", 0))
	}
}

// TestSubjectSearchIgnoresThePersonWhoTouchesEverything checks looking up a
// subject returns the people concentrated in it rather than the people who
// appear everywhere. Ranked on raw weight the busiest few come back for every
// subject in the organization, which is the same defect that has had to be
// fixed in ownership, departure, adjacency, and joined work.
func TestSubjectSearchIgnoresThePersonWhoTouchesEverything(t *testing.T) {
	t.Parallel()
	recs := []connector.Record{{
		Kind: connector.KindPerson, Name: "Owner", Email: "owner@x.com",
		Topics: []string{"zigbee", "zigbee", "zigbee"}, Source: "git",
	}}
	sweep := []string{"zigbee", "zigbee", "zigbee", "zigbee"}
	for i := range 80 {
		sweep = append(sweep, fmt.Sprintf("area%d", i), fmt.Sprintf("area%d", i))
	}
	recs = append(recs, connector.Record{
		Kind: connector.KindPerson, Name: "Sweeper", Email: "sweeper@x.com",
		Topics: sweep, Source: "git",
	})
	ix := index.New()
	ix.Build(recs)
	ix.Canonicalize()

	got := Search(ix, "zigbee", 5)
	if len(got) == 0 {
		t.Fatal("zigbee matched nobody")
	}
	if got[0].Name != "Owner" {
		var names []string
		for _, r := range got {
			names = append(names, r.Name)
		}
		t.Errorf("search order = %v, want the person concentrated in zigbee first", names)
	}
}
