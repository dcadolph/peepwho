package index

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/connector"
)

// TestSynonymSearch checks that a question in one vocabulary finds the person
// the index knows in another: "time off" reaches the vacation owner, "k8s"
// reaches the kubernetes owner, and the synonym neither outranks an exact
// match nor dilutes anyone's strength.
func TestSynonymSearch(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Name: "Vera Hall", Email: "vera@x.com", Topics: []string{"vacation"}},
		{Name: "Ken Ochoa", Email: "ken@x.com", Topics: []string{"kubernetes"}},
		{Name: "Tess Boyd", Email: "tess@x.com", Topics: []string{"kafka"}},
	})

	tests := []struct {
		Query      string
		WantFirst  string
		WantReason string
	}{{ // Test 0: A two-word synonym reaches the owner of the other word.
		Query: "who do I talk to about time off", WantFirst: "vera@x.com",
		WantReason: `vacation (topic) for "time off"`,
	}, { // Test 1: An abbreviation reaches the full name.
		Query: "who knows k8s", WantFirst: "ken@x.com",
		WantReason: `kubernetes (topic) for "k8s"`,
	}, { // Test 2: The exact word still works exactly as before.
		Query: "who knows about vacation", WantFirst: "vera@x.com",
		WantReason: "vacation (topic)",
	}, { // Test 3: A query with no synonyms behaves as it always did.
		Query: "kafka", WantFirst: "tess@x.com",
		WantReason: "kafka (topic)",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := ix.Search(test.Query, 3)
			if len(got) == 0 {
				t.Fatalf("Search(%q) returned nothing", test.Query)
			}
			if string(got[0].Person.ID) != test.WantFirst {
				t.Errorf("Search(%q) top = %s, want %s", test.Query, got[0].Person.ID, test.WantFirst)
			}
			if !strings.Contains(strings.Join(got[0].Reasons, "; "), test.WantReason) {
				t.Errorf("Search(%q) reasons = %v, want to contain %q",
					test.Query, got[0].Reasons, test.WantReason)
			}
		})
	}

	// An exact match must outrank a synonym for the same subject: someone who
	// says "vacation" beats the synonym path when both name the same person,
	// and strength through a synonym still counts the asked words as covered.
	viaSynonym := ix.Search("time off", 1)
	viaExact := ix.Search("vacation", 1)
	if len(viaSynonym) == 0 || len(viaExact) == 0 {
		t.Fatal("both searches should find the vacation owner")
	}
	if viaSynonym[0].Score >= viaExact[0].Score {
		t.Errorf("synonym score %v should stay below the exact score %v",
			viaSynonym[0].Score, viaExact[0].Score)
	}
	if c := viaSynonym[0].Strength; c <= 0.5 {
		t.Errorf("strength through a synonym = %v: the asked words were covered, so it must not dilute", c)
	}
}

// TestExpandTermsDeterministic pins the expansion order for a question that
// touches several synonym groups. The order feeds scoring, where per-person
// floats accumulate term by term, so an unstable order changes the last bits
// of scores between two runs of one query on one index, and same input, same
// output, byte for byte, is a product claim.
func TestExpandTermsDeterministic(t *testing.T) {
	t.Parallel()
	expansions := func() []string {
		var out []string
		for _, e := range expandTerms("vacation and payroll and kubernetes questions") {
			out = append(out, e.term)
		}
		return out
	}
	first := expansions()
	if len(first) == 0 {
		t.Fatal("the query triggered no expansions; the fixture is vacuous")
	}
	for range 50 {
		if diff := cmp.Diff(first, expansions()); diff != "" {
			t.Fatalf("expansion order changed between runs of one query (-first +now):\n%s", diff)
		}
	}
}
