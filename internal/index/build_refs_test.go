package index

import (
	"bytes"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/invindex"
	"github.com/dcadolph/whotoask/internal/model"
)

// TestMemberAndManagerReferencesNormalize covers references to people carried
// inside other records. A member listed as alice+ci@x.com and the person keyed
// alice@x.com are one mailbox; without the email fold the member silently
// drops out of membership and co-membership, and a manager written with a plus
// tag dangles instead of resolving to the person it names.
func TestMemberAndManagerReferencesNormalize(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Alice", Email: "alice@x.com",
			Topics: []string{"billing"}, Source: "org-csv"},
		{Kind: connector.KindPerson, Name: "Kim Doe", Email: "kim.doe@corp.com", Source: "org-csv"},
		{Kind: connector.KindPerson, Name: "Rep Orter", Email: "rep@corp.com",
			Manager: "Kim.Doe+x@corp.com", Source: "org-csv"},
		{Kind: connector.KindChannel, Name: "billing", Members: []string{"alice+ci@x.com"}, Source: "slack"},
	})

	ch := ix.Graph.Channels["billing"]
	if ch == nil {
		t.Fatal("the billing channel is missing")
	}
	if !slices.Contains(ch.Members, model.ID("alice@x.com")) {
		t.Errorf("channel members = %v, want the plus-tagged member folded to alice@x.com", ch.Members)
	}
	rep := ix.Graph.People["rep@corp.com"]
	if rep == nil {
		t.Fatal("the reporting person is missing")
	}
	if rep.ManagerID != "kim.doe@corp.com" {
		t.Errorf("manager id = %s, want the plus tag folded to kim.doe@corp.com", rep.ManagerID)
	}

	// A purge must recognize the plus-tagged reference too, or the membership
	// survives as a leftover mention of the person being erased.
	alice := ix.FindPerson("alice@x.com")
	if alice == nil {
		t.Fatal("alice is missing")
	}
	res := ix.Forget(alice)
	if res.Mentions == 0 {
		t.Error("the plus-tagged membership was not stripped on purge")
	}
}

// TestNegativeWeightNeverWritesNegativePostings covers a hand-fed record
// carrying a negative weight. Written through, it produces negative postings
// and topic affinities, which nothing in the ranking rules is defined over;
// it is floored to the single-observation default instead, byte for byte.
func TestNegativeWeightNeverWritesNegativePostings(t *testing.T) {
	t.Parallel()
	build := func(w float64) *Index {
		ix := New()
		ix.Build([]connector.Record{{Kind: connector.KindPerson, Name: "Kim", Email: "kim@x.com",
			Topics: []string{"billing"}, Weight: w, Source: "jsonsrc"}})
		return ix
	}
	neg, one := build(-2), build(1)

	for tid, aff := range neg.Graph.People["kim@x.com"].Topics {
		if aff <= 0 {
			t.Errorf("topic %s affinity = %v, want positive", tid, aff)
		}
	}
	if diff := cmp.Diff(one.Graph.People["kim@x.com"].Topics, neg.Graph.People["kim@x.com"].Topics); diff != "" {
		t.Errorf("affinities differ from the weight-one build (-one +neg):\n%s", diff)
	}
	if !bytes.Equal(invindex.EncodePostings(one.postings), invindex.EncodePostings(neg.postings)) {
		t.Error("postings differ from the weight-one build")
	}
}
