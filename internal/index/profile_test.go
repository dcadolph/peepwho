package index

import (
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
)

// TestProfileMinimal verifies a person with no team, org, manager, or channels
// still profiles, with the optional fields left empty.
func TestProfileMinimal(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Email: "solo@x.com", Name: "Solo", Source: "org-csv",
	}})

	got, ok := ix.Profile("solo@x.com")
	if !ok {
		t.Fatal("Profile(solo) not found")
	}
	if got.Person == nil || got.Person.Name != "Solo" {
		t.Errorf("person = %+v, want Solo", got.Person)
	}
	if got.Team != nil || got.Org != nil || got.Manager != nil || len(got.Channels) != 0 {
		t.Errorf("optional fields should be empty for a bare person: %+v", got)
	}
}
