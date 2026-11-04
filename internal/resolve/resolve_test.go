package resolve

import (
	"context"
	"github.com/dcadolph/peepwho/internal/model"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
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
