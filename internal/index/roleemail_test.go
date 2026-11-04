package index

import (
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
)

// TestRoleEmailDoesNotMergePeople checks that two distinct people who share a
// real mailbox merge into one, while two who share a role or team mailbox stay
// separate, so a shared support@ address does not collapse the org.
func TestRoleEmailDoesNotMergePeople(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Email      string
		WantPeople int
	}{
		{"real.person@x.com", 1},
		{"support@x.com", 2},
	}
	for _, test := range tests {
		t.Run(test.Email, func(t *testing.T) {
			t.Parallel()
			ix := New()
			ix.Build([]connector.Record{
				{Kind: connector.KindPerson, PersonID: "src:u1", Name: "Alpha", Email: test.Email, Source: "t"},
				{Kind: connector.KindPerson, PersonID: "src:u2", Name: "Beta", Email: test.Email, Source: "t"},
			})
			ix.Canonicalize()
			if got := len(ix.Graph.People); got != test.WantPeople {
				t.Errorf("people = %d, want %d", got, test.WantPeople)
			}
		})
	}
}
