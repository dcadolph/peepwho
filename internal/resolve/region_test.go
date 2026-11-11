package resolve

import (
	"fmt"
	"slices"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// tied returns a record tying one subject to several others.
func tied(name string, to ...string) connector.Record {
	r := connector.Record{Kind: connector.KindTopic, Name: name, Source: "git"}
	for _, t := range to {
		r.Links = append(r.Links, connector.TopicLink{To: t, Weight: 0.5})
	}
	return r
}

// held returns a person holding each subject as many times as it is repeated.
func held(name, email string, topics ...string) connector.Record {
	return connector.Record{
		Kind: connector.KindPerson, Name: name, Email: email,
		Topics: topics, Source: "git",
	}
}

// TestRegionsFindWorkThatMovesTogether checks whotoask reports a connected body
// of work resting on one person as a single finding. Per-subject risk shows
// three separate small problems where there is really one large one: whoever
// takes the work over has to learn all of it at once.
func TestRegionsFindWorkThatMovesTogether(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		held("Ada", "ada@x.com", "dlna", "dlna", "dmr", "dmr", "dms", "dms"),
		tied("dlna", "dmr", "dms"),
		tied("dmr", "dlna"),
		tied("dms", "dlna"),
	})
	ix.Canonicalize()

	got := Regions(ix, 5)
	if len(got) != 1 {
		t.Fatalf("regions = %+v, want the three joined subjects reported as one", got)
	}
	if got[0].Lead != "Ada" {
		t.Errorf("lead = %q, want Ada", got[0].Lead)
	}
	for _, want := range []string{"dlna", "dmr", "dms"} {
		if !slices.Contains(got[0].Topics, want) {
			t.Errorf("region = %v, want %q in it", got[0].Topics, want)
		}
	}
}

// TestRegionStopsWhereTheLeadChanges checks a region is one person's, not the
// whole graph. Ties run on past the edge of what somebody leads, and following
// them would merge every connected subject in the organization into one finding
// that names nobody.
func TestRegionStopsWhereTheLeadChanges(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		held("Ada", "ada@x.com", "dlna", "dlna", "dmr", "dmr", "dms", "dms"),
		held("Bo", "bo@x.com", "kasa", "kasa", "sense", "sense", "emulated", "emulated"),
		tied("dlna", "dmr", "dms"),
		tied("dms", "kasa"), // the bridge between the two people's work
		tied("kasa", "sense", "emulated"),
	})
	ix.Canonicalize()

	for _, r := range Regions(ix, 5) {
		hasAda := slices.Contains(r.Topics, "dlna")
		hasBo := slices.Contains(r.Topics, "kasa")
		if hasAda && hasBo {
			t.Errorf("region %v spans two people's work, want it to stop at the edge", r.Topics)
		}
	}
}

// TestRegionsIgnoreThePersonWhoTouchesEverything is the guard that has had to
// be written four times in four places. Ranking a subject by raw weight hands
// every subject in a code base to whoever touches all of it, and the regions
// then belong to that person instead of to the people who actually own them.
func TestRegionsIgnoreThePersonWhoTouchesEverything(t *testing.T) {
	t.Parallel()
	recs := []connector.Record{
		held("Owner", "owner@x.com", "dlna", "dlna", "dmr", "dmr", "dms", "dms"),
		tied("dlna", "dmr", "dms"),
		tied("dmr", "dlna"),
		tied("dms", "dlna"),
	}
	// Somebody who touches every area in the company, this one included, harder
	// than its owner does.
	sweep := []string{"dlna", "dlna", "dlna", "dmr", "dmr", "dmr", "dms", "dms", "dms"}
	for i := range 80 {
		sweep = append(sweep, fmt.Sprintf("area%d", i), fmt.Sprintf("area%d", i))
	}
	recs = append(recs, held("Sweeper", "sweeper@x.com", sweep...))

	ix := index.New()
	ix.Build(recs)
	ix.Canonicalize()

	got := Regions(ix, 5)
	if len(got) == 0 {
		t.Fatal("the joined work was not reported at all")
	}
	if got[0].Lead != "Owner" {
		t.Errorf("lead = %q, want Owner, who does this work rather than all work", got[0].Lead)
	}
}

// TestDepartureNamesTheWholeRegion checks what leaves with somebody is measured
// against who leads the work, not who has the most raw weight in it. Reading it
// off raw weight let the people who touch everything out-weigh a maintainer on
// their own subjects, so the report named one subject where the person really
// led nine, which is the opposite of useful in an offboarding conversation.
func TestDepartureNamesTheWholeRegion(t *testing.T) {
	t.Parallel()
	recs := []connector.Record{
		held("Owner", "owner@x.com", "dlna", "dlna", "dmr", "dmr", "dms", "dms"),
		tied("dlna", "dmr", "dms"),
		tied("dmr", "dlna"),
		tied("dms", "dlna"),
	}
	sweep := []string{"dlna", "dlna", "dlna", "dmr", "dmr", "dmr", "dms", "dms", "dms"}
	for i := range 80 {
		sweep = append(sweep, fmt.Sprintf("area%d", i), fmt.Sprintf("area%d", i))
	}
	recs = append(recs, held("Sweeper", "sweeper@x.com", sweep...))

	ix := index.New()
	ix.Build(recs)
	ix.Canonicalize()

	imp := Departure(ix, "Owner")
	if len(imp.Regions) != 1 {
		t.Fatalf("regions = %+v, want the joined work named as one thing", imp.Regions)
	}
	if imp.Regions[0].Size() != 3 {
		t.Errorf("region spans %d subjects, want all 3 they lead", imp.Regions[0].Size())
	}
	if len(imp.Sole)+len(imp.Top) == 0 {
		t.Error("nothing at all was reported as leaving with them")
	}
}

// TestRiskMarksAnExpertWhoHasMovedOn checks a concentrated subject says whether
// the person holding it is still in it. A subject resting on one person is a
// risk; a subject resting on one person who has already stopped working on it
// is a sharper one, and the two are indistinguishable without this.
func TestRiskMarksAnExpertWhoHasMovedOn(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Gone", Email: "gone@x.com",
			Topics: []string{"billing", "billing"}, RecentTopics: []string{"other"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Here", Email: "here@x.com",
			Topics: []string{"ledger", "ledger"}, RecentTopics: []string{"ledger", "ledger"}, Source: "git"},
	})
	ix.Canonicalize()

	byTopic := map[string]TopicRisk{}
	for _, r := range Risk(ix, 0) {
		byTopic[r.Topic] = r
	}
	billing, ok := byTopic["billing"]
	if !ok || len(billing.Experts) == 0 {
		t.Fatalf("billing was not scored: %+v", byTopic)
	}
	if !billing.Experts[0].Quiet {
		t.Error("billing rests on somebody who stopped working on it, and the report does not say so")
	}
	ledger, ok := byTopic["ledger"]
	if !ok || len(ledger.Experts) == 0 {
		t.Fatalf("ledger was not scored: %+v", byTopic)
	}
	if ledger.Experts[0].Quiet {
		t.Error("ledger's expert is still in it and was wrongly marked as gone")
	}
}

// TestRiskMarksADeactivatedExpert checks that a subject held by somebody whose
// account is deactivated reports them as gone, even when their last work on it
// was recent, and still counts their share, since a departed sole expert is
// the sharpest knowledge risk there is.
func TestRiskMarksADeactivatedExpert(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Left", Email: "left@x.com", PersonID: "slack:u1",
			Topics: []string{"billing", "billing"}, RecentTopics: []string{"billing", "billing"},
			Source: "slack", Account: model.AccountDeactivated},
		{Kind: connector.KindPerson, Name: "Here", Email: "here@x.com", PersonID: "slack:u2",
			Topics: []string{"ledger", "ledger"}, RecentTopics: []string{"ledger", "ledger"},
			Source: "slack", Account: model.AccountActive},
	})
	ix.Canonicalize()

	byTopic := map[string]TopicRisk{}
	for _, r := range Risk(ix, 0) {
		byTopic[r.Topic] = r
	}
	billing, ok := byTopic["billing"]
	if !ok || len(billing.Experts) == 0 {
		t.Fatalf("billing was not scored: %+v", byTopic)
	}
	if !billing.Experts[0].Quiet || !billing.LeadQuiet() {
		t.Error("billing rests on a deactivated account and is not reported as gone")
	}
	if billing.BusFactor != 1 {
		t.Errorf("billing bus factor = %d, want 1: the departed expert still holds it alone", billing.BusFactor)
	}
	if ledger := byTopic["ledger"]; len(ledger.Experts) == 0 || ledger.Experts[0].Quiet {
		t.Errorf("ledger = %+v, want its active expert not marked as gone", ledger)
	}
}
