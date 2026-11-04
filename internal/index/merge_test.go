package index

import (
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/model"
)

// TestMergeAccumulatesTheSamePerson checks a second reading of somebody adds to
// what is held rather than replacing it. Incremental re-indexing folds a delta
// into what a full read left behind, and getting this wrong either erases
// months of evidence or double-counts a single week of it.
func TestMergeAccumulatesTheSamePerson(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Email: "ada@x.com", Name: "Ada",
		Topics: []string{"kafka"}, Source: "slack",
	}})
	ix.Canonicalize()
	before := ix.Graph.People["ada@x.com"].Topics[model.ID("kafka")]

	ix.MergeIncremental([]connector.Record{{
		Kind: connector.KindPerson, Email: "ada@x.com", Name: "Ada",
		Topics: []string{"kafka"}, Source: "slack",
	}})
	ix.Canonicalize()

	if got := len(ix.Graph.People); got != 1 {
		t.Fatalf("graph holds %d people, want the same person folded into one", got)
	}
	after := ix.Graph.People["ada@x.com"].Topics[model.ID("kafka")]
	if after <= before {
		t.Errorf("kafka weight %v after a second reading, was %v: the delta was dropped", after, before)
	}
}

// TestMergeKeepsSubjectTies checks the ties between subjects survive an
// incremental run. They arrive on their own kind of record, and folding those
// as if they were people both keyed them wrongly and threw the ties away, so a
// re-index quietly lost the graph of what is worked on together.
func TestMergeKeepsSubjectTies(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Email: "ada@x.com", Name: "Ada",
			Topics: []string{"billing", "ledger"}, Source: "git"},
		{Kind: connector.KindTopic, Name: "billing", Source: "git",
			Links: []connector.TopicLink{{To: "ledger", Weight: 0.4}}},
	})
	ix.Canonicalize()

	ix.MergeIncremental([]connector.Record{
		{Kind: connector.KindTopic, Name: "billing", Source: "git",
			Links: []connector.TopicLink{{To: "ledger", Weight: 0.6}, {To: "invoices", Weight: 0.3}}},
	})
	ix.Canonicalize()

	billing := ix.Graph.Topics[model.ID("billing")]
	if billing == nil {
		t.Fatal("billing left the graph entirely")
		return
	}
	if got := billing.Near[model.ID("ledger")].Weight; got != 0.6 {
		t.Errorf("billing to ledger = %v, want the stronger claim of 0.6", got)
	}
	if got := billing.Near[model.ID("invoices")].Weight; got != 0.3 {
		t.Errorf("billing to invoices = %v, want the new tie carried in", got)
	}
}

// TestSubjectAndPersonDoNotCollide checks a subject is not folded into a person
// who happens to resolve to the same name. They were keyed the same way, so a
// subject called billing and somebody identified as billing accumulated into
// each other.
func TestSubjectAndPersonDoNotCollide(t *testing.T) {
	t.Parallel()
	person := connector.Record{Kind: connector.KindPerson, Name: "billing", Source: "git",
		Topics: []string{"invoices"}}
	subject := connector.Record{Kind: connector.KindTopic, Name: "billing", Source: "git",
		Links: []connector.TopicLink{{To: "ledger", Weight: 0.5}}}
	if foldKey(person) == foldKey(subject) {
		t.Errorf("a person and a subject both key as %q, so they fold into each other", foldKey(person))
	}
}
