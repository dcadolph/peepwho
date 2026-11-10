package resolve

import (
	"fmt"
	"slices"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
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
