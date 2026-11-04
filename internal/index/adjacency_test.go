package index

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/peepwho/internal/connector"

	"github.com/dcadolph/peepwho/internal/model"
)

func TestFoldGroupName(t *testing.T) {
	t.Parallel()
	tests := []struct{ In, Want string }{
		{"store-admin", "store"},
		{"store-write", "store"},
		{"Store-Read", "store"},
		{"payments", "payments"},
		{"x-dev", "x"},
		{"-admin", "-admin"},
	}
	for _, test := range tests {
		t.Run(test.In, func(t *testing.T) {
			t.Parallel()
			if got := foldGroupName(test.In); got != test.Want {
				t.Errorf("foldGroupName(%q) = %q, want %q", test.In, got, test.Want)
			}
		})
	}
}

func TestIsOrgWide(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Size, Total int
		Want        bool
	}{
		{250, 300, true},  // over the absolute cap
		{201, 1000, true}, // over the absolute cap
		{50, 100, true},   // over 40% of a large org
		{30, 100, false},  // under 40%
		{5, 10, false},    // small org: fraction rule does not apply
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := isOrgWide(test.Size, test.Total); got != test.Want {
				t.Errorf("isOrgWide(%d, %d) = %v, want %v", test.Size, test.Total, got, test.Want)
			}
		})
	}
}

// TestNearRanksBySharedGroupsAndTopics builds a small graph where one person
// shares a team, a channel, and a topic with the focal person and confirms they
// rank first, ahead of people who share only one signal.
func TestNearRanksBySharedGroupsAndTopics(t *testing.T) {
	t.Parallel()
	ix := New()
	g := ix.Graph
	g.Teams["payments"] = &model.Team{ID: "payments", Name: "payments"}
	g.Topics["t1"] = &model.Topic{ID: "t1", Name: "billing"}
	g.Topics["t2"] = &model.Topic{ID: "t2", Name: "kafka"}
	g.People["alice"] = &model.Person{ID: "alice", Name: "Alice", TeamID: "payments", Topics: map[model.ID]float64{"t1": 1, "t2": 1}}
	g.People["bob"] = &model.Person{ID: "bob", Name: "Bob", TeamID: "payments", Topics: map[model.ID]float64{"t1": 1}}
	g.People["carol"] = &model.Person{ID: "carol", Name: "Carol", Topics: map[model.ID]float64{}}
	g.People["dave"] = &model.Person{ID: "dave", Name: "Dave", Topics: map[model.ID]float64{"t2": 1}}
	g.Channels["billing"] = &model.Channel{ID: "billing", Name: "billing", Members: []model.ID{"alice", "bob", "carol"}}

	near := ix.Near("alice", 10)
	if len(near) != 3 {
		t.Fatalf("near count = %d, want 3 (%+v)", len(near), near)
	}
	if near[0].ID != "bob" {
		t.Errorf("nearest = %s, want bob", near[0].ID)
	}
	if len(near[0].Reasons) == 0 {
		t.Errorf("nearest has no reasons")
	}
	// Bob shares a team, a channel, and a topic, so he must outscore the rest.
	if !(near[0].Score > near[1].Score) {
		t.Errorf("bob score %.3f not greater than next %.3f", near[0].Score, near[1].Score)
	}
	// The focal person is never in their own results.
	for _, a := range near {
		if a.ID == "alice" {
			t.Errorf("focal person appeared in results")
		}
	}
}

// TestNearIsNotTheSamePeopleForEveryone checks proximity is measured against
// how much each person knows rather than as a raw total. Every organization has
// a few people who touch all of it, and by raw overlap they are near everybody:
// whoever you asked about, the answer came back as the same handful of names.
func TestNearIsNotTheSamePeopleForEveryone(t *testing.T) {
	t.Parallel()
	recs := []connector.Record{
		// Two pairs who genuinely work alongside each other.
		{Kind: connector.KindPerson, Name: "Radio One", Email: "r1@x.com",
			Topics: []string{"zigbee", "zigbee", "thread"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Radio Two", Email: "r2@x.com",
			Topics: []string{"zigbee", "zigbee", "thread"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Storage One", Email: "s1@x.com",
			Topics: []string{"recorder", "recorder", "history"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Storage Two", Email: "s2@x.com",
			Topics: []string{"recorder", "recorder", "history"}, Source: "git"},
	}
	// And somebody who touches everything, including both pairs.
	sweep := []string{"zigbee", "thread", "recorder", "history"}
	for i := range 60 {
		sweep = append(sweep, fmt.Sprintf("area%d", i), fmt.Sprintf("area%d", i))
	}
	recs = append(recs, connector.Record{
		Kind: connector.KindPerson, Name: "Sweeper", Email: "sweep@x.com",
		Topics: sweep, Source: "git",
	})

	ix := New()
	ix.Build(recs)
	ix.Canonicalize()

	nearest := func(who model.ID) string {
		got := ix.Near(who, 1)
		if len(got) == 0 {
			t.Fatalf("%s has nobody near them", who)
		}
		return got[0].Name
	}
	if got := nearest("r1@x.com"); got != "Radio Two" {
		t.Errorf("nearest to Radio One = %q, want their counterpart rather than the busiest person", got)
	}
	if got := nearest("s1@x.com"); got != "Storage Two" {
		t.Errorf("nearest to Storage One = %q, want their counterpart rather than the busiest person", got)
	}
}

// TestNearReasonsAreDeterministic pins which shared groups and topics a reason
// names when there are more than fit. The rankings tie-break on id, but a
// candidate sharing six groups used to show a different four on different
// runs, and every answer carrying its reasons means the same answer carries
// the same reasons. Scores are compared exactly too, since accumulation order
// fixes their last float bits.
func TestNearReasonsAreDeterministic(t *testing.T) {
	t.Parallel()
	ix := New()
	g := ix.Graph
	for _, name := range []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"} {
		g.Channels[model.ID(name)] = &model.Channel{
			ID: model.ID(name), Name: name, Members: []model.ID{"focal", "buddy"},
		}
	}
	focalTopics := make(map[model.ID]float64)
	buddyTopics := make(map[model.ID]float64)
	for i, tid := range []model.ID{"ta", "tb", "tc", "td", "te", "tf"} {
		g.Topics[tid] = &model.Topic{ID: tid, Name: "subject " + string(tid), Curated: true}
		focalTopics[tid] = float64(i + 1)
		buddyTopics[tid] = float64(i + 1)
	}
	g.People["focal"] = &model.Person{ID: "focal", Name: "Focal", Topics: focalTopics}
	g.People["buddy"] = &model.Person{ID: "buddy", Name: "Buddy", Topics: buddyTopics}

	first := ix.Near("focal", 5)
	if len(first) != 1 {
		t.Fatalf("near count = %d, want 1 (%+v)", len(first), first)
	}
	want := []string{
		"shares #alpha, #bravo, #charlie, #delta",
		"topics: subject ta, subject tb, subject tc, subject td",
	}
	if diff := cmp.Diff(want, first[0].Reasons); diff != "" {
		t.Errorf("reasons are not the first items in sorted order (-want +got):\n%s", diff)
	}
	for range 30 {
		if diff := cmp.Diff(first, ix.Near("focal", 5)); diff != "" {
			t.Fatalf("Near changed between runs on one graph (-first +now):\n%s", diff)
		}
	}
}

// TestFindPeopleReportsNamesakes covers the lookup a purge depends on: two
// people sharing a name must both come back, in id order, and FindPerson must
// return the same one of them every run. Deleting a nondeterministically
// chosen namesake is the failure this guards.
func TestFindPeopleReportsNamesakes(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Jane Roe", Email: "jane.a@x.com", Source: "org-csv"},
		{Kind: connector.KindPerson, Name: "Jane Roe", Email: "jane.b@x.com", Source: "org-csv"},
		{Kind: connector.KindPerson, Name: "Solo Person", Email: "solo@x.com", Source: "org-csv"},
	})

	people := ix.FindPeople("Jane Roe")
	if len(people) != 2 {
		t.Fatalf("FindPeople found %d people named Jane Roe, want 2", len(people))
	}
	if people[0].ID != "jane.a@x.com" || people[1].ID != "jane.b@x.com" {
		t.Errorf("FindPeople order = [%s %s], want id order", people[0].ID, people[1].ID)
	}
	for range 20 {
		got := ix.FindPerson("Jane Roe")
		if got == nil || got.ID != "jane.a@x.com" {
			t.Fatalf("FindPerson on a namesake tie did not stay on the lowest id: %+v", got)
		}
	}
	if got := ix.FindPeople("solo@x.com"); len(got) != 1 || got[0].ID != "solo@x.com" {
		t.Errorf("FindPeople by email = %+v, want exactly the keyed person", got)
	}
	if got := ix.FindPeople("Nobody Here"); len(got) != 0 {
		t.Errorf("FindPeople for an unknown name = %+v, want none", got)
	}
}
