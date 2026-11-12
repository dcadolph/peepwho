package resolve

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// TestKeywordResolve verifies the keyword resolver returns ranked matches.
func TestKeywordResolve(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Name: "Jane Roe", Email: "jane@x.com", Title: "Engineer",
			Team: "Billing", Topics: []string{"retries"}},
	})

	got, err := NewKeyword(ix).Resolve(context.Background(), "retries", 5)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got.People) != 1 || got.People[0].Person.Email != "jane@x.com" {
		t.Fatalf("got %v, want one person match for jane@x.com", got.People)
	}
}

// TestKeywordResolveFillsMembersAfterTheChatterCut checks a channel's members
// are ranked for the channels the answer keeps. A channel that only mentions
// the word is dropped by the chatter filter, and each channel left lists the
// members the channel search gives it.
func TestKeywordResolveFillsMembersAfterTheChatterCut(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Name: "Jane Roe", Email: "jane@x.com", Topics: []string{"retries"}},
		{Name: "Bob Lee", Email: "bob@x.com", Topics: []string{"kafka"}},
		{Name: "Ann Poe", Email: "ann@x.com", Topics: []string{"kafka", "retries"}},
		{Kind: connector.KindChannel, Name: "billing", Title: "retries and dunning",
			Members: []string{"jane@x.com", "bob@x.com", "ann@x.com"}},
		{Kind: connector.KindChannel, Name: "streams", Topics: []string{"kafka"},
			Members: []string{"bob@x.com", "ann@x.com"}},
		{Kind: connector.KindChannel, Name: "ops", Text: "kafka retries pager",
			Members: []string{"jane@x.com", "bob@x.com"}},
	})

	tests := []struct {
		Query        string
		WantChannels []string
	}{{ // Test 0: The channel that only mentions retries is dropped.
		Query: "retries", WantChannels: []string{"billing"},
	}, { // Test 1: The channel that only mentions kafka is dropped.
		Query: "kafka", WantChannels: []string{"streams"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			searched := make(map[string][]model.ID)
			for _, m := range ix.SearchChannels(test.Query, 0) {
				searched[m.Channel.Name] = memberIDs(m.TopMembers)
			}
			got, err := NewKeyword(ix).Resolve(context.Background(), test.Query, 5)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			var names []string
			for _, c := range got.Channels {
				names = append(names, c.Channel.Name)
				want := searched[c.Channel.Name]
				if len(want) == 0 {
					t.Fatalf("no members listed on %s, so the case proves nothing", c.Channel.Name)
				}
				if diff := cmp.Diff(want, memberIDs(c.TopMembers)); diff != "" {
					t.Errorf("members of %s mismatch (-search +resolve):\n%s", c.Channel.Name, diff)
				}
			}
			if diff := cmp.Diff(test.WantChannels, names); diff != "" {
				t.Errorf("channels mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// memberIDs lists the ids of people in order.
func memberIDs(ps []*model.Person) []model.ID {
	var out []model.ID
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

// TestNewKeywordNil verifies the constructor panics on a nil index.
func TestNewKeywordNil(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("NewKeyword(nil) did not panic")
		}
	}()
	NewKeyword(nil)
}

// TestReorderIgnoresAnAmbiguousName checks a display name shared by two
// candidates moves neither. Indexed by name alone the second overwrote the
// first, so a reordering naming "Kim" silently promoted whichever happened to
// be stored last, which is the wrong person half the time.
func TestReorderIgnoresAnAmbiguousName(t *testing.T) {
	t.Parallel()
	cands := []model.Match{
		{Person: &model.Person{ID: "kim-a@x.com", Name: "Kim", Email: "kim-a@x.com"}},
		{Person: &model.Person{ID: "kim-b@x.com", Name: "Kim", Email: "kim-b@x.com"}},
		{Person: &model.Person{ID: "ada@x.com", Name: "Ada", Email: "ada@x.com"}},
	}
	// Naming the ambiguous "Kim" must not reorder anything, so the original
	// order survives.
	got := reorderPeople(cands, []string{"Kim"})
	if len(got) != 3 || got[0].Person.ID != "kim-a@x.com" {
		t.Errorf("order = %v, want the ambiguous name to move nobody", ids(got))
	}
	// An unambiguous name still works, and so does the position.
	got = reorderPeople(cands, []string{"Ada"})
	if got[0].Person.ID != "ada@x.com" {
		t.Errorf("order = %v, want Ada first", ids(got))
	}
	got = reorderPeople(cands, []string{"2"})
	if got[0].Person.ID != "kim-b@x.com" {
		t.Errorf("order = %v, want the second candidate first", ids(got))
	}
	// And the email names either Kim exactly.
	got = reorderPeople(cands, []string{"kim-b@x.com"})
	if got[0].Person.ID != "kim-b@x.com" {
		t.Errorf("order = %v, want the emailed Kim first", ids(got))
	}
}

// ids lists match ids for a readable failure.
func ids(ms []model.Match) []string {
	var out []string
	for _, m := range ms {
		out = append(out, string(m.Person.ID))
	}
	return out
}
