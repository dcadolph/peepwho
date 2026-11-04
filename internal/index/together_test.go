package index

import (
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/model"
)

// TestTopicLinksAreCarriedIntoTheGraph checks a source's observation that two
// subjects are worked on together survives into the graph, where relatedness
// can use it. It is the only thing peepwho knows about a subject that does not
// come through the people holding it.
func TestTopicLinksAreCarriedIntoTheGraph(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Ada", Email: "ada@x.com",
			Topics: []string{"billing", "ledger"}, Source: "git"},
		{Kind: connector.KindTopic, Name: "billing", Source: "git",
			Links: []connector.TopicLink{{To: "ledger", Weight: 0.4}}},
	})
	ix.Canonicalize()

	billing := ix.Graph.Topics[model.ID("billing")]
	if billing == nil {
		t.Fatal("billing is not in the graph")
		return
	}
	if got := billing.Near[model.ID("ledger")].Weight; got != 0.4 {
		t.Errorf("billing to ledger = %v, want the observed tie of 0.4", got)
	}
}

// TestATieDoesNotMakeASubject checks being changed alongside something does not
// on its own establish a subject. Only a person holding it, or a source stating
// it, does that: otherwise any word a tokenizer produced would become a subject
// by being adjacent to one.
func TestATieDoesNotMakeASubject(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Ada", Email: "ada@x.com",
			Topics: []string{"billing"}, Source: "git"},
		{Kind: connector.KindTopic, Name: "billing", Source: "git",
			Links: []connector.TopicLink{{To: "scratchpad", Weight: 0.9}}},
	})
	ix.Canonicalize()

	scratch := ix.Graph.Topics[model.ID("scratchpad")]
	if scratch != nil && scratch.Salient() {
		t.Error("a subject nobody holds became salient purely by being tied to one")
	}
}
