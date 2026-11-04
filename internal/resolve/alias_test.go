package resolve

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
)

// splitIx builds a graph holding one person under three addresses that the
// identity rules leave apart, plus an unrelated person.
func splitIx(t *testing.T) *index.Index {
	t.Helper()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Dana Reed", Email: "dana@corp.com",
			Topics: []string{"billing", "ledger", "invoices", "dunning"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Dana Reed", Email: "dana1@corp.com",
			Topics: []string{"billing"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Dana Reed", Email: "dana2@corp.com",
			Topics: []string{"ledger"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Sam Okoye", Email: "sam@corp.com",
			Topics: []string{"kafka"}, Source: "git"},
	})
	ix.AutoJoin()
	ix.Canonicalize()
	return ix
}

// TestSuggestAliasesLeadsWithTheHeaviestRecord verifies the canonical id is the
// record already carrying the most work, so accepting a suggestion rewrites the
// fewest identifiers and names the person a reader recognizes.
func TestSuggestAliasesLeadsWithTheHeaviestRecord(t *testing.T) {
	t.Parallel()
	ix := splitIx(t)
	got := SuggestAliases(ix, SplitNames(ix))
	if len(got) != 1 {
		t.Fatalf("suggestions = %d, want 1 group for the split name", len(got))
	}
	if got[0].Canonical != "dana@corp.com" {
		t.Errorf("canonical = %q, want the record with the most subjects", got[0].Canonical)
	}
	want := []string{"dana1@corp.com", "dana2@corp.com"}
	if diff := cmp.Diff(want, got[0].Aliases, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("aliases (-want +got):\n%s", diff)
	}
	if got[0].Why == "" {
		t.Error("no reason given; a suggestion a reader cannot judge is one they must trust")
	}
}

// TestSuggestAliasesLeavesDistinctPeopleAlone verifies somebody who shares no
// name with anyone is never proposed for joining.
func TestSuggestAliasesLeavesDistinctPeopleAlone(t *testing.T) {
	t.Parallel()
	ix := splitIx(t)
	for _, s := range SuggestAliases(ix, SplitNames(ix)) {
		if slices.Contains(s.Aliases, "sam@corp.com") || s.Canonical == "sam@corp.com" {
			t.Errorf("proposed joining an unrelated person: %+v", s)
		}
	}
}

// TestAliasFileClosesTheSplit is the round trip the deliverable promises: the
// suggestions are written in the format the alias loader reads, and feeding
// them back joins the records the rules left apart.
//
// A report that names a problem and hands over the correction has done the
// reader's next step. One that only names it is a complaint, and the reader
// has no way to tell how much the caveat cost them.
func TestAliasFileClosesTheSplit(t *testing.T) {
	t.Parallel()
	ix := splitIx(t)
	before := len(ix.Graph.People)
	suggestions := SuggestAliases(ix, SplitNames(ix))
	file := AliasFile(suggestions)
	if len(file) == 0 {
		t.Fatal("no alias file produced for a split identity")
	}

	// Rebuild applying the suggestions the way --aliases does.
	again := index.New()
	for canonical, aliases := range file {
		for _, alias := range aliases {
			again.Alias(model.ID(canonical), model.ID(alias))
		}
	}
	again.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Dana Reed", Email: "dana@corp.com",
			Topics: []string{"billing", "ledger", "invoices", "dunning"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Dana Reed", Email: "dana1@corp.com",
			Topics: []string{"billing"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Dana Reed", Email: "dana2@corp.com",
			Topics: []string{"ledger"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Sam Okoye", Email: "sam@corp.com",
			Topics: []string{"kafka"}, Source: "git"},
	})
	again.AutoJoin()
	again.Canonicalize()

	if got := len(again.Graph.People); got >= before {
		t.Errorf("people after applying the aliases = %d, was %d: the file joined nothing",
			got, before)
	}
	if left := SplitNames(again); len(left) != 0 {
		t.Errorf("splits remaining = %+v, want none once the suggestions are applied", left)
	}
}

// TestSuggestAliasesOrdersByEvidence verifies the group holding the most
// subjects between it is offered first, since that is the split distorting the
// most findings.
func TestSuggestAliasesOrdersByEvidence(t *testing.T) {
	t.Parallel()
	ix := index.New()
	var recs []connector.Record
	for i := range 2 {
		recs = append(recs, connector.Record{
			Kind: connector.KindPerson, Name: "Big Split",
			Email: fmt.Sprintf("big%d@corp.com", i), Source: "git",
			Topics: []string{"alpha", "beta", "gamma", "delta", "epsilon"},
		})
		recs = append(recs, connector.Record{
			Kind: connector.KindPerson, Name: "Small Split",
			Email: fmt.Sprintf("small%d@corp.com", i), Source: "git",
			Topics: []string{"zeta"},
		})
	}
	ix.Build(recs)
	ix.AutoJoin()
	ix.Canonicalize()

	got := SuggestAliases(ix, SplitNames(ix))
	if len(got) < 2 {
		t.Fatalf("suggestions = %d, want both groups", len(got))
	}
	if got[0].Name != "Big Split" {
		t.Errorf("first suggestion = %q, want the group holding the most subjects", got[0].Name)
	}
}
