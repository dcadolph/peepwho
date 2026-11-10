package resolve

import (
	"fmt"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
)

// TestRiskLeadResistsSweepers pins the difference between who has touched a
// subject and who it rests on. A person who touches the whole code base
// out-holds the owner of nearly every subject by raw share; the Lead field
// must name the concentrated owner anyway, and the exposure view must follow
// the lead. Measured on kubernetes, getting this wrong named the same five
// people for half of all subjects.
func TestRiskLeadResistsSweepers(t *testing.T) {
	t.Parallel()
	ix := index.New()
	var recs []connector.Record
	// The sweeper touches thirty subjects, including the contested one, with
	// more raw weight everywhere than anyone else.
	sweep := make([]string, 0, 90)
	for i := range 30 {
		top := fmt.Sprintf("area%02d", i)
		sweep = append(sweep, top, top, top)
	}
	sweep = append(sweep, "billing", "billing", "billing")
	recs = append(recs, connector.Record{
		Kind: connector.KindPerson, Name: "Sweeper", Email: "sweep@x.com",
		Topics: sweep, Source: "t",
	})
	// The owner works billing and almost nothing else.
	recs = append(recs, connector.Record{
		Kind: connector.KindPerson, Name: "Owner", Email: "owner@x.com",
		Topics: []string{"billing", "billing"}, Source: "t",
	})
	// A third person so billing is not a two-name curiosity.
	recs = append(recs, connector.Record{
		Kind: connector.KindPerson, Name: "Passer", Email: "passer@x.com",
		Topics: []string{"billing"}, Source: "t",
	})
	ix.Build(recs)
	ix.Canonicalize()

	var billing *TopicRisk
	for _, r := range Risk(ix, 0) {
		if r.Topic == "billing" {
			b := r
			billing = &b
		}
	}
	if billing == nil {
		t.Fatal("billing was not scored")
		return
	}
	if billing.Experts[0].Name != "Sweeper" {
		t.Errorf("Experts[0] = %q; the fixture no longer makes the sweeper the top share",
			billing.Experts[0].Name)
	}
	if billing.Lead != "Owner" {
		t.Errorf("Lead = %q, want Owner: breadth must discount the sweeper", billing.Lead)
	}
	if billing.LeadID == "" {
		t.Error("LeadID is empty")
	}
}
