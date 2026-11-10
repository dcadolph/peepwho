package index

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/connector"
)

// TestDiff verifies joiners, leavers, and channel changes are detected.
func TestDiff(t *testing.T) {
	t.Parallel()
	prev := New()
	prev.Build([]connector.Record{
		{Name: "Jane Roe", Email: "jane@x.com", Team: "Billing"},
		{Name: "Old Guy", Email: "old@x.com", Team: "Legacy"},
		{Kind: connector.KindChannel, Name: "oldchan"},
	})
	cur := New()
	cur.Build([]connector.Record{
		{Name: "Jane Roe", Email: "jane@x.com", Team: "Billing"},
		{Name: "New Person", Email: "new@x.com", Team: "Billing"},
		{Kind: connector.KindChannel, Name: "newchan"},
	})

	c := Diff(prev.Graph, cur.Graph)
	if !slices.Contains(c.PeopleJoined, "New Person") {
		t.Errorf("PeopleJoined = %v, want New Person", c.PeopleJoined)
	}
	if !slices.Contains(c.PeopleLeft, "Old Guy") {
		t.Errorf("PeopleLeft = %v, want Old Guy", c.PeopleLeft)
	}
	if !slices.Contains(c.ChannelsAdded, "newchan") || !slices.Contains(c.ChannelsRemoved, "oldchan") {
		t.Errorf("channels added=%v removed=%v", c.ChannelsAdded, c.ChannelsRemoved)
	}
	if !slices.Contains(c.TeamsRemoved, "Legacy") {
		t.Errorf("TeamsRemoved = %v, want Legacy", c.TeamsRemoved)
	}
	if c.Empty() {
		t.Error("changes should not be empty")
	}
}

// TestDiffSkipsTeams verifies a CODEOWNERS team added or removed is not a
// person joining or leaving.
func TestDiffSkipsTeams(t *testing.T) {
	t.Parallel()
	prev := New()
	prev.Build([]connector.Record{
		{Source: "codeowners", PersonID: "codeowners:acme/old-squad", Name: "@acme/old-squad"},
	})
	cur := New()
	cur.Build([]connector.Record{
		{Source: "codeowners", PersonID: "codeowners:acme/new-squad", Name: "@acme/new-squad"},
		{Source: "codeowners", PersonID: "codeowners:zoe", Name: "@zoe"},
	})
	c := Diff(prev.Graph, cur.Graph)
	if diff := cmp.Diff([]string{"@zoe"}, c.PeopleJoined, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("PeopleJoined mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string(nil), c.PeopleLeft, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("PeopleLeft mismatch (-want +got):\n%s", diff)
	}
}

// TestDiffEmpty verifies an unchanged graph reports no changes.
func TestDiffEmpty(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{{Name: "A", Email: "a@x.com"}})
	if !Diff(ix.Graph, ix.Graph).Empty() {
		t.Error("identical graph should diff to empty")
	}
}

// TestDiffRecognizesRekeying covers the person whose canonical id changes when
// a later source adds their email: the same human used to be reported as one
// person leaving and another joining. Only exact identifier overlap joins the
// two sides; a shared display name and a shared role mailbox both stay churn,
// because different people really do carry those.
func TestDiffRecognizesRekeying(t *testing.T) {
	t.Parallel()
	prev := New()
	prev.Build([]connector.Record{
		{Kind: connector.KindPerson, PersonID: "U123", Name: "Jane Roe", Source: "slack"},
		{Kind: connector.KindPerson, Email: "old@x.com", Name: "Old Guy", Source: "slack"},
		{Kind: connector.KindPerson, Email: "sam1@x.com", Name: "Sam Kim", Source: "slack"},
		{Kind: connector.KindPerson, PersonID: "bot1", Email: "oncall@x.com", Name: "Pager Bot",
			Source: "slack"},
	})
	cur := New()
	cur.Build([]connector.Record{
		{Kind: connector.KindPerson, PersonID: "U123", Name: "Jane Roe", Source: "slack"},
		{Kind: connector.KindPerson, PersonID: "U123", Email: "jane@x.com", Name: "Jane Roe",
			Source: "org"},
		{Kind: connector.KindPerson, Email: "new@x.com", Name: "New Person", Source: "slack"},
		{Kind: connector.KindPerson, Email: "sam2@x.com", Name: "Sam Kim", Source: "slack"},
		{Kind: connector.KindPerson, PersonID: "bot2", Email: "oncall@x.com", Name: "Alert Bot",
			Source: "slack"},
	})

	c := Diff(prev.Graph, cur.Graph)
	if slices.Contains(c.PeopleJoined, "Jane Roe") || slices.Contains(c.PeopleLeft, "Jane Roe") {
		t.Errorf("a re-keyed person reported as churn: joined=%v left=%v", c.PeopleJoined, c.PeopleLeft)
	}
	if !slices.Contains(c.PeopleJoined, "New Person") {
		t.Errorf("PeopleJoined = %v, want New Person", c.PeopleJoined)
	}
	if !slices.Contains(c.PeopleLeft, "Old Guy") {
		t.Errorf("PeopleLeft = %v, want Old Guy", c.PeopleLeft)
	}
	// Two different Sam Kims swapping is churn, not a re-keying: a name alone
	// never joins two identities.
	if !slices.Contains(c.PeopleJoined, "Sam Kim") || !slices.Contains(c.PeopleLeft, "Sam Kim") {
		t.Errorf("same-named strangers were hidden: joined=%v left=%v", c.PeopleJoined, c.PeopleLeft)
	}
	// Two automation accounts sharing a role mailbox are two accounts: the
	// shared mailbox is not identity.
	if !slices.Contains(c.PeopleJoined, "Alert Bot") || !slices.Contains(c.PeopleLeft, "Pager Bot") {
		t.Errorf("role-mailbox accounts were hidden: joined=%v left=%v", c.PeopleJoined, c.PeopleLeft)
	}
}

// TestChangesMerge verifies merged changes are the sorted union per list and
// that the later side of a pair wins over the earlier one.
func TestChangesMerge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Earlier    Changes
		Later      Changes
		WantResult Changes
	}{{ // Test 0: Disjoint changes union and sort.
		Earlier:    Changes{PeopleJoined: []string{"Zoe"}, TeamsRemoved: []string{"core"}},
		Later:      Changes{PeopleJoined: []string{"Ann", "Zoe"}, ChannelsAdded: []string{"#ops"}},
		WantResult: Changes{PeopleJoined: []string{"Ann", "Zoe"}, TeamsRemoved: []string{"core"}, ChannelsAdded: []string{"#ops"}},
	}, { // Test 1: Joined then left reads as left.
		Earlier:    Changes{PeopleJoined: []string{"Ann", "Bo"}},
		Later:      Changes{PeopleLeft: []string{"Ann"}},
		WantResult: Changes{PeopleJoined: []string{"Bo"}, PeopleLeft: []string{"Ann"}},
	}, { // Test 2: Left then joined reads as joined, for teams and channels too.
		Earlier: Changes{PeopleLeft: []string{"Ann"}, TeamsRemoved: []string{"core"}, ChannelsRemoved: []string{"#ops"}},
		Later:   Changes{PeopleJoined: []string{"Ann"}, TeamsAdded: []string{"core"}, ChannelsAdded: []string{"#ops"}},
		WantResult: Changes{PeopleJoined: []string{"Ann"}, TeamsAdded: []string{"core"},
			ChannelsAdded: []string{"#ops"}},
	}, { // Test 3: Both empty.
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := test.Earlier.Merge(test.Later)
			if diff := cmp.Diff(test.WantResult, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("merge mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
