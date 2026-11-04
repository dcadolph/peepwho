package index

import (
	"fmt"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
)

// TestBuildJoinsByAlternateID verifies an alternate identifier a source knows
// for a person joins them with another source that keys the person by that id,
// and that unrelated ids stay separate.
func TestBuildJoinsByAlternateID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name    string
		Records []connector.Record
		Want    int
	}{{ // Test 0: an alt id links two sources into one person.
		Records: []connector.Record{
			{Kind: connector.KindPerson, Email: "john@corp.com", Name: "John Smith", Source: "graph",
				AltIDs: []string{"john.smith@corp.onmicrosoft.com"}},
			{Kind: connector.KindPerson, Email: "john.smith@corp.onmicrosoft.com", Source: "slack"},
		},
		Want: 1,
	}, { // Test 1: no shared id, so they stay separate.
		Records: []connector.Record{
			{Kind: connector.KindPerson, Email: "john@corp.com", Source: "graph", AltIDs: []string{"jsmith@ad.corp"}},
			{Kind: connector.KindPerson, Email: "jane@corp.com", Source: "slack"},
		},
		Want: 2,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ix := New()
			ix.Build(test.Records)
			ix.Canonicalize()
			if got := len(ix.Graph.People); got != test.Want {
				t.Errorf("people = %d, want %d", got, test.Want)
			}
		})
	}
}
