package resolve

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestWithoutChatter(t *testing.T) {
	t.Parallel()
	// chatterItem stands in for a match: a name and whether it is declared.
	type chatterItem struct {
		Name     string
		Declared bool
	}
	tests := []struct {
		WantResult []chatterItem
		In         []chatterItem
	}{{ // Test 0: A declared leader drops every undeclared match after it.
		In: []chatterItem{
			{"owner", true}, {"chatter", false}, {"backup", true}, {"passerby", false},
		},
		WantResult: []chatterItem{{"owner", true}, {"backup", true}},
	}, { // Test 1: An undeclared leader keeps everything, declared matches below included.
		In: []chatterItem{
			{"talker", false}, {"team", true}, {"other", false},
		},
		WantResult: []chatterItem{{"talker", false}, {"team", true}, {"other", false}},
	}, { // Test 2: Nothing declared keeps every match.
		In:         []chatterItem{{"a", false}, {"b", false}},
		WantResult: []chatterItem{{"a", false}, {"b", false}},
	}, { // Test 3: Everything declared keeps every match.
		In:         []chatterItem{{"a", true}, {"b", true}},
		WantResult: []chatterItem{{"a", true}, {"b", true}},
	}, { // Test 4: An empty list stays empty.
		In: nil, WantResult: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			in := append([]chatterItem(nil), test.In...)
			got := withoutChatter(in, func(c chatterItem) bool { return c.Declared })
			if diff := cmp.Diff(test.WantResult, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
