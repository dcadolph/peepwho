package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
)

// joinFixture builds an index whose AutoJoin infers a unique-name join and a
// team-corroborated join, for the identity view and renderer tests.
func joinFixture(t *testing.T) *index.Index {
	t.Helper()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Kevin Novak", Email: "kevin@corp.com", Team: "Payments", Source: "t"},
		{Kind: connector.KindPerson, PersonID: "github:kevinnovak", Name: "@kevinnovak", Source: "github"},
		{Kind: connector.KindPerson, Name: "Angela Malone", Email: "angela@corp.com", Team: "Payments", Source: "t"},
		{Kind: connector.KindPerson, Name: "Angela Malone", Email: "angela@sales.com", Team: "Sales", Source: "t"},
		{Kind: connector.KindPerson, PersonID: "pagerduty:angela-malone", Name: "@angela-malone", Team: "Payments", Source: "pagerduty"},
	})
	ix.AutoJoin()
	ix.Canonicalize()
	return ix
}

// TestBuildIdentityView checks the view groups joins by person with the right
// confidence and evidence, in name order.
func TestBuildIdentityView(t *testing.T) {
	t.Parallel()
	v := buildIdentityView(joinFixture(t))
	if v.Merges != 2 {
		t.Fatalf("merges = %d, want 2", v.Merges)
	}
	if len(v.People) != 2 || v.People[0].Name != "Angela Malone" || v.People[1].Name != "Kevin Novak" {
		t.Fatalf("people = %+v, want Angela then Kevin", v.People)
	}
	kevin := v.People[1]
	if len(kevin.Joins) != 1 || kevin.Joins[0].Alias != "github:kevinnovak" ||
		kevin.Joins[0].Confidence != 0.9 || kevin.Joins[0].Reason != "unique name match" {
		t.Errorf("kevin joins = %+v, want the github unique-name join at 0.9", kevin.Joins)
	}
}

// TestRenderIdentity checks the human renderer shows the header, evidence, and
// aliases, and the clear message when there is nothing to show.
func TestRenderIdentity(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	renderIdentity(&buf, buildIdentityView(joinFixture(t)), style{})
	out := buf.String()
	for _, want := range []string{"Identity joins", "2 inferred across 2 people",
		"github:kevinnovak", "unique name match", "pagerduty:angela-malone", "name and shared team"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	var empty bytes.Buffer
	renderIdentity(&empty, identityView{}, style{})
	if !strings.Contains(empty.String(), "No inferred identity merges") {
		t.Errorf("empty output = %q, want the no-merges message", empty.String())
	}
}

// TestFilterIdentityView checks the person filter narrows to the matching person
// and returns an empty view when nothing matches.
func TestFilterIdentityView(t *testing.T) {
	t.Parallel()
	v := buildIdentityView(joinFixture(t))
	got := filterIdentityView(v, "kevin")
	if len(got.People) != 1 || got.People[0].Name != "Kevin Novak" || got.Merges != 1 {
		t.Errorf("filter kevin = %+v (merges %d), want just Kevin with 1 merge", got.People, got.Merges)
	}
	if none := filterIdentityView(v, "zzz"); len(none.People) != 0 || none.Merges != 0 {
		t.Errorf("filter zzz = %+v, want empty", none)
	}
}

// TestUnlinkedOwnersAreAWorklist checks the owners with no recorded work are
// reported as something to fix, ordered by how much rests on them, and that
// groups are left out. A group named as an owner can never be tied to an
// address, so listing it buries the people somebody could reconnect.
func TestUnlinkedOwnersAreAWorklist(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		// Declared, and nothing else: their work is under an address peepwho
		// has not tied to this handle.
		{Kind: connector.KindPerson, PersonID: "codeowners:big", Name: "big",
			Topics: []string{"alpha", "beta", "gamma"}, Source: "codeowners"},
		{Kind: connector.KindPerson, PersonID: "codeowners:small", Name: "small",
			Topics: []string{"delta"}, Source: "codeowners"},
		// A group, which no alias can ever resolve.
		{Kind: connector.KindPerson, PersonID: "codeowners:acme/platform", Name: "acme/platform",
			Topics: []string{"epsilon"}, Source: "codeowners"},
		// Declared and active, so not on the list at all.
		{Kind: connector.KindPerson, PersonID: "codeowners:busy", Name: "busy",
			Topics: []string{"zeta"}, Source: "codeowners"},
		{Kind: connector.KindPerson, PersonID: "codeowners:busy", Name: "busy",
			Topics: []string{"zeta", "zeta"}, Source: "git"},
	})
	ix.Canonicalize()

	view := buildUnlinkedView(ix)
	var ids []string
	for _, o := range view.Owners {
		ids = append(ids, o.ID)
	}
	if len(view.Owners) != 2 {
		t.Fatalf("unlinked = %v, want only the two inactive people", ids)
	}
	if view.Owners[0].ID != "codeowners:big" {
		t.Errorf("first = %q, want the owner of the most areas first", view.Owners[0].ID)
	}
	for _, id := range ids {
		if strings.Contains(id, "/") {
			t.Errorf("unlinked = %v, want the group left out", ids)
		}
		if id == "codeowners:busy" {
			t.Errorf("unlinked = %v, want the active owner left out", ids)
		}
	}
	if view.Declared != 3 {
		t.Errorf("declared = %d, want the three people and not the group", view.Declared)
	}
}
