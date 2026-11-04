package index

import (
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
)

// tieFixture builds an index where billing-retries and kafka-lag travel
// together in the work, billing-retries has its own expert, and kafka-lag is
// held by somebody the question never names.
func tieFixture(t *testing.T) *Index {
	t.Helper()
	ix := New()
	var recs []connector.Record
	// Enough co-occurring work to make the tie real and both subjects salient.
	for i := 0; i < 8; i++ {
		recs = append(recs,
			connector.Record{Kind: connector.KindPerson, Name: "Billing Owner", Email: "billing@corp.com",
				Topics: []string{"billing-retries"}, Source: "git"},
			connector.Record{Kind: connector.KindPerson, Name: "Kafka Owner", Email: "kafka@corp.com",
				Topics: []string{"kafka-lag"}, Source: "jira"},
		)
	}
	recs = append(recs,
		connector.Record{Kind: connector.KindPerson, Name: "Crossing Person", Email: "cross@corp.com",
			Topics: []string{"billing-retries", "kafka-lag"}, Source: "git"},
		connector.Record{Kind: connector.KindTopic, Name: "billing-retries", Source: "git",
			Links: []connector.TopicLink{{To: "kafka-lag", Weight: 0.3, Witnesses: 1, Sole: "cross@corp.com"}}},
	)
	ix.Build(recs)
	ix.Canonicalize()
	return ix
}

// TestAskReachesThroughTheTieGraph covers the search path reading the
// co-occurrence graph. Before this, peepwho's most distinctive structure fed
// the risk views only, and a question about billing retries could not surface
// the person holding the subject it travels with.
func TestAskReachesThroughTheTieGraph(t *testing.T) {
	t.Parallel()
	ix := tieFixture(t)

	got := ix.Search("billing retries", 0)
	var kafka bool
	var kafkaReason string
	for _, m := range got {
		if m.Person.Email == "kafka@corp.com" {
			kafka = true
			kafkaReason = strings.Join(m.Reasons, " · ")
		}
	}
	if !kafka {
		t.Fatalf("the kafka-lag owner never surfaced for a billing-retries question; matches: %d", len(got))
	}
	// The reason has to say WHY, or the expansion reads as a wrong answer.
	if !strings.Contains(kafkaReason, "travels with") {
		t.Errorf("reason %q does not say the subjects travel together", kafkaReason)
	}
	// Reach never outranks what was asked: the named subject's owner stays first.
	if got[0].Person.Email == "kafka@corp.com" {
		t.Error("a tie expansion outranked the subject the question actually named")
	}
}

// TestTieExpansionStaysBounded checks the trade stays small: a query that
// names no salient subject expands into nothing.
func TestTieExpansionStaysBounded(t *testing.T) {
	t.Parallel()
	ix := tieFixture(t)
	covers := map[string][]string{"zzz": {"zzz"}}
	asked := map[string]string{}
	added := ix.tieExpand([]string{"zzz"}, covers, asked)
	if len(added) != 0 {
		t.Errorf("a query naming nothing expanded into %v", added)
	}
}
