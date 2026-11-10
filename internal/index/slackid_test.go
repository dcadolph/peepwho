package index

import (
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/model"
)

// TestSlackIDResolvesToPerson verifies a Slack user ID joins the person it
// belongs to, so an asker known only by their Slack ID can be recognized. The
// canonical identifier stays the email, and the Slack ID is kept as an alias,
// lowercased like every other identifier.
func TestSlackIDResolvesToPerson(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{{
		Kind:     connector.KindPerson,
		Source:   "slack",
		PersonID: "slack:U1",
		Name:     "Jane Roe",
		Email:    "jane@x.com",
		Title:    "Staff Engineer",
	}})

	if got := ix.Canonical("slack:u1"); got != model.ID("jane@x.com") {
		t.Errorf("Canonical(slack:u1) = %q, want jane@x.com", got)
	}
	p := ix.Graph.People["jane@x.com"]
	if p == nil {
		t.Fatalf("no person keyed by email; people = %v", ix.Graph.People)
		return
	}
	found := false
	for _, id := range p.Identities {
		if id == "slack:u1" {
			found = true
		}
	}
	if !found {
		t.Errorf("identities = %v, want slack:u1 among them", p.Identities)
	}
}

// TestSlackIDWithoutEmail verifies a user whose profile hides their email
// still resolves, keyed by the Slack ID itself.
func TestSlackIDWithoutEmail(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{{
		Kind:     connector.KindPerson,
		Source:   "slack",
		PersonID: "slack:U9",
		Name:     "No Email",
	}})
	if got := ix.Canonical("slack:u9"); got != model.ID("slack:u9") {
		t.Errorf("Canonical(slack:u9) = %q, want slack:u9", got)
	}
}
