package util

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestDistinctKeepsFirstAndOrder checks the first of each key survives and the
// order is the order things arrived in, which is what every caller relied on
// when they wrote this loop themselves.
func TestDistinctKeepsFirstAndOrder(t *testing.T) {
	t.Parallel()
	type person struct {
		Name string
		ID   string
	}
	tests := []struct {
		Name string
		In   []person
		Want []person
	}{{ // Test 0: A repeat keeps the first, not the last.
		Name: "repeats",
		In:   []person{{"Ada", "a"}, {"Bo", "b"}, {"Ada again", "a"}},
		Want: []person{{"Ada", "a"}, {"Bo", "b"}},
	}, { // Test 1: An empty key is dropped rather than deduped against itself.
		Name: "empty keys",
		In:   []person{{"Nobody", ""}, {"Ada", "a"}, {"Nameless", ""}},
		Want: []person{{"Ada", "a"}},
	}, { // Test 2: Nothing in, nothing out, and no nil surprises.
		Name: "empty input", In: nil, Want: []person{},
	}}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			got := Distinct(test.In, func(p person) string { return p.ID })
			if diff := cmp.Diff(test.Want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
