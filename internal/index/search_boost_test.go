package index

import (
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
)

// boostFixture builds an index where the question's own subject has a strong
// owner, one passer-by matched a single typed word in passing prose, and a
// strongly evidenced expert is reachable only through the tie graph.
func boostFixture(t *testing.T) *Index {
	t.Helper()
	ix := New()
	var recs []connector.Record
	for range 8 {
		recs = append(recs,
			connector.Record{Kind: connector.KindPerson, Name: "Billing Owner", Email: "billing@corp.com",
				Topics: []string{"billing-retries"}, Source: "git"},
			connector.Record{Kind: connector.KindPerson, Name: "Kafka Owner", Email: "kafka@corp.com",
				Topics: []string{"kafka-lag"}, Source: "jira"},
		)
	}
	recs = append(recs,
		connector.Record{Kind: connector.KindPerson, Name: "Passer By", Email: "passer@corp.com",
			Text: "will look at retries later", Source: "slack"},
		connector.Record{Kind: connector.KindPerson, Name: "Crossing Person", Email: "cross@corp.com",
			Topics: []string{"billing-retries", "kafka-lag"}, Source: "git"},
		connector.Record{Kind: connector.KindTopic, Name: "billing-retries", Source: "git",
			Links: []connector.TopicLink{{To: "kafka-lag", Weight: 0.3, Witnesses: 1, Sole: "cross@corp.com"}}},
	)
	ix.Build(recs)
	ix.Canonicalize()
	return ix
}

// TestEvidenceBoostKeepsTieReachBelowTheQuestion pins the promise that a
// candidate found only through the tie graph never outranks anyone who
// matched the question itself, through the evidence-boost re-sort as well as
// the first ranking. The passer-by holds one weak text mention of a typed
// word; the kafka owner enters through the tie graph with strong topic
// evidence and a higher boosted score, and must still rank below them.
func TestEvidenceBoostKeepsTieReachBelowTheQuestion(t *testing.T) {
	t.Parallel()
	ix := boostFixture(t)

	got := ix.Search("billing retries", 0)
	if len(got) == 0 {
		t.Fatal("the query matched nobody; the fixture is broken")
	}
	if got[0].Person.Email != "billing@corp.com" {
		t.Errorf("top result = %s, want the owner of the subject actually named", got[0].Person.Email)
	}
	rank := map[string]int{}
	for i, m := range got {
		rank[m.Person.Email] = i + 1
	}
	kafka, passer := rank["kafka@corp.com"], rank["passer@corp.com"]
	if kafka == 0 {
		t.Fatal("the kafka-lag owner never surfaced; the tie expansion did not run")
	}
	if passer == 0 {
		t.Fatal("the passer-by never surfaced; the fixture is broken")
	}
	if kafka < passer {
		t.Errorf("a tie-derived candidate (rank %d) outranked someone who matched the question (rank %d)",
			kafka, passer)
	}
}
