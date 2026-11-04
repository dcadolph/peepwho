package index

import (
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/model"
)

// TestForgetLeavesNoTraceInTheSavedFile is the test the privacy claim rests on.
//
// Forget resets the graph, the postings, and the reason text, and three things
// used to live outside all three and be persisted by name anyway: the person's
// embedding vector, keyed by their address; the join ledger, naming the alias,
// the canonical id, and the evidence for merging them; and the sole witness on
// a tie between two subjects, which was re-derived afterwards so the risk view
// went on naming a person who had been erased. The command's own words are
// "no trace of them", which has to mean the file on disk.
func TestForgetLeavesNoTraceInTheSavedFile(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Kim Doe", Email: "kim@corp.com",
			Topics: []string{"billing", "ledger", "invoices"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Kim Doe", Email: "kim.doe@personal.com",
			Topics: []string{"billing", "ledger", "invoices"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Ada Lane", Email: "ada@corp.com",
			Topics: []string{"billing"}, Source: "git"},
		{Kind: connector.KindChannel, Name: "payments", Source: "slack",
			Members: []string{"kim+tag@corp.com", "ada@corp.com"}},
		{Kind: connector.KindTopic, Name: "billing", Source: "git", Links: []connector.TopicLink{
			{To: "ledger", Weight: 0.4, Witnesses: 1, Sole: "kim@corp.com"},
		}},
	})
	ix.AutoJoin()
	ix.Canonicalize()

	// An embedding, the way an --embed install holds one.
	var target model.ID
	for id, p := range ix.Graph.People {
		if strings.EqualFold(p.Name, "Kim Doe") {
			target = id
		}
	}
	if target == "" {
		t.Fatal("the fixture person is not in the graph")
	}
	ix.personVecs[target] = []float32{0.1, 0.2, 0.3}
	if len(ix.Joins()) == 0 {
		t.Fatal("the fixture's two records did not join, so the ledger proves nothing")
	}

	ix.Forget(ix.Graph.People[target])

	if v, ok := ix.personVecs[target]; ok {
		t.Errorf("an embedding vector survived the purge under key %s: %v", target, v)
	}
	for _, j := range ix.Joins() {
		if strings.Contains(strings.ToLower(string(j.Alias)), "kim") ||
			strings.Contains(strings.ToLower(string(j.Canonical)), "kim") {
			t.Errorf("a join ledger entry survived naming the purged person: %+v", j)
		}
	}
	for _, recs := range ix.sources {
		for _, rec := range recs {
			for _, link := range rec.Links {
				if strings.Contains(strings.ToLower(link.Sole), "kim") {
					t.Errorf("a tie still names the purged person as its sole witness: %+v", link)
				}
			}
			for _, m := range rec.Members {
				if strings.Contains(strings.ToLower(m), "kim") {
					t.Errorf("a channel still lists the purged person: %q", m)
				}
			}
		}
	}
	// The tie itself is a fact about two subjects and survives; only the name
	// attached to it goes.
	kept := false
	for _, recs := range ix.sources {
		for _, rec := range recs {
			if rec.Kind == connector.KindTopic && len(rec.Links) > 0 {
				kept = true
			}
		}
	}
	if !kept {
		t.Error("the tie between the subjects was dropped along with the name on it")
	}
	if _, ok := ix.Graph.People[target]; ok {
		t.Error("the person is still in the graph")
	}
}
