package resolve

import (
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// successorIx builds a graph with a sole holder, a near colleague, a distant
// one, and a reviewer who writes nothing.
func successorIx(t *testing.T) *index.Index {
	t.Helper()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Holder", Email: "holder@x.com",
			Topics: []string{"billing", "ledger", "invoices", "dunning"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Near", Email: "near@x.com",
			Topics: []string{"billing", "ledger", "invoices"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Far", Email: "far@x.com",
			Topics: []string{"kafka", "streams", "billing"}, Source: "git"},
	})
	ix.Canonicalize()
	return ix
}

// TestSuccessorForPrefersTheAdjacentColleague verifies the person sharing most
// of the holder's subjects is named ahead of one sharing a single word, and
// that the reasons say why in printable prose.
func TestSuccessorForPrefersTheAdjacentColleague(t *testing.T) {
	t.Parallel()
	ix := successorIx(t)
	in := NewSuccessorInputs(ix, nil, nil)
	got, ok := in.SuccessorFor(ix, "holder@x.com", "")
	if !ok {
		t.Fatal("no successor named, want the adjacent colleague")
	}
	if got.Name != "Near" {
		t.Errorf("successor = %q, want Near: three shared subjects beat one", got.Name)
	}
	if len(got.Reasons) == 0 || !strings.Contains(got.Reasons[0].Detail, "3 of the 4") {
		t.Errorf("reasons = %+v, want the overlap counted as 3 of the 4", got.Reasons)
	}
}

// TestSuccessorForPlaceEvidenceOutranksOverlap verifies somebody already
// working in the holder's directory is named over a wordier overlap, because
// working there is the strongest preparation the record can show.
func TestSuccessorForPlaceEvidenceOutranksOverlap(t *testing.T) {
	t.Parallel()
	ix := successorIx(t)
	places := []Place{{
		Dir: "internal/billing",
		Holders: []PlaceHolder{
			{ID: "holder@x.com", Name: "Holder", Work: 40},
			{ID: "far@x.com", Name: "Far", Work: 6},
		},
	}}
	in := NewSuccessorInputs(ix, places, nil)
	got, ok := in.SuccessorFor(ix, "holder@x.com", "internal/billing")
	if !ok {
		t.Fatal("no successor named")
	}
	if got.Name != "Far" {
		t.Errorf("successor = %q, want Far: real work in the place beats overlap", got.Name)
	}
	found := false
	for _, r := range got.Reasons {
		if r.Kind == "place" && strings.Contains(r.Detail, "internal/billing") {
			found = true
		}
	}
	if !found {
		t.Errorf("reasons = %+v, want the place named", got.Reasons)
	}
}

// TestSuccessorForReviewerCounts verifies a person who only reviews still
// gathers evidence, which is what surfaces the approver who writes nothing.
func TestSuccessorForReviewerCounts(t *testing.T) {
	t.Parallel()
	ix := successorIx(t)
	reviewers := map[string]map[model.ID]bool{
		"internal/billing": {"far@x.com": true},
	}
	in := NewSuccessorInputs(ix, nil, reviewers)
	got, ok := in.SuccessorFor(ix, "holder@x.com", "internal/billing")
	if !ok {
		t.Fatal("no successor named")
	}
	if got.Name != "Far" {
		t.Errorf("successor = %q, want the reviewer Far", got.Name)
	}
}

// TestSuccessorForNobodyClose verifies the floor: when nobody shares anything,
// the answer is false rather than the least-bad stranger, because naming a
// person on no evidence is the failure this feature must never have.
func TestSuccessorForNobodyClose(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Alone", Email: "alone@x.com",
			Topics: []string{"billing", "ledger"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Stranger", Email: "s@x.com",
			Topics: []string{"kafka"}, Source: "git"},
	})
	ix.Canonicalize()
	in := NewSuccessorInputs(ix, nil, nil)
	if got, ok := in.SuccessorFor(ix, "alone@x.com", ""); ok {
		t.Errorf("named %q on no shared evidence, want nobody", got.Name)
	}
}

// TestSuccessorForNeverNamesTheHolder verifies the holder cannot be their own
// successor however the evidence lands.
func TestSuccessorForNeverNamesTheHolder(t *testing.T) {
	t.Parallel()
	ix := successorIx(t)
	places := []Place{{
		Dir:     "internal/billing",
		Holders: []PlaceHolder{{ID: "holder@x.com", Name: "Holder", Work: 40}},
	}}
	in := NewSuccessorInputs(ix, places, nil)
	got, ok := in.SuccessorFor(ix, "holder@x.com", "internal/billing")
	if ok && got.ID == "holder@x.com" {
		t.Error("the holder was named as their own successor")
	}
}
