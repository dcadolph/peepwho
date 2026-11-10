package resolve

import (
	"fmt"
	"slices"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
)

// TestOwnershipDrift checks that a CODEOWNERS-declared owner who is not the
// strongest actual expert is reported as drift, and that a declared owner who is
// the top expert is not.
func TestOwnershipDrift(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		// Alice owns payments on paper (CODEOWNERS), but does little of the work.
		{Kind: connector.KindPerson, Name: "Alice", Topics: []string{"payments"}, Source: "codeowners"},
		// Bob actually does the payments work.
		{Kind: connector.KindPerson, Name: "Bob", Email: "bob@x.com", Topics: []string{"payments", "payments", "payments"}, Source: "slack"},
		// Carol owns and does the auth work: no drift there.
		{Kind: connector.KindPerson, Name: "Carol", Email: "carol@x.com", Topics: []string{"auth"}, Source: "codeowners"},
		{Kind: connector.KindPerson, Name: "Carol", Email: "carol@x.com", Topics: []string{"auth", "auth"}, Source: "slack"},
	})
	ix.Canonicalize()

	report := Ownership(ix)
	drift := report.Drift
	if len(drift) != 1 {
		t.Fatalf("drift = %+v, want exactly payments", drift)
	}
	d := drift[0]
	if d.Topic != "payments" || d.Actual != "Bob" || !slices.Equal(d.Declared, []string{"Alice"}) {
		t.Errorf("drift = %+v, want payments declared [Alice] actual Bob", d)
	}
}

// TestDriftIgnoresThePersonWhoTouchesEverything checks an area is reported as
// moving to whoever really works on it, not to the busiest person in the
// organization. Every code base has a few people who touch everything, and by
// raw share they out-hold the declared owner of nearly every area at once.
// Reporting that as a thousand ownership changes says nothing about ownership.
func TestDriftIgnoresThePersonWhoTouchesEverything(t *testing.T) {
	t.Parallel()
	recs := []connector.Record{
		// The owner of record, who has stopped doing the work.
		{Kind: connector.KindPerson, Name: "Alice", Email: "alice@x.com",
			Topics: []string{"payments"}, Source: "codeowners"},
		// The person the area has actually moved to: it is most of what they do.
		{Kind: connector.KindPerson, Name: "Bob", Email: "bob@x.com",
			Topics: []string{"payments", "payments", "payments", "payments"}, Source: "git"},
	}
	// And somebody who touches every area in the company, including this one,
	// slightly harder than Bob does.
	sweeper := []string{"payments", "payments", "payments", "payments", "payments"}
	for i := range 60 {
		sweeper = append(sweeper, fmt.Sprintf("area%d", i), fmt.Sprintf("area%d", i))
	}
	recs = append(recs, connector.Record{
		Kind: connector.KindPerson, Name: "Sweeper", Email: "sweeper@x.com",
		Topics: sweeper, Source: "git",
	})

	ix := index.New()
	ix.Build(recs)
	ix.Canonicalize()

	report := Ownership(ix)
	var payments *OwnerDrift
	for i := range report.Drift {
		if report.Drift[i].Topic == "payments" {
			payments = &report.Drift[i]
		}
	}
	if payments == nil {
		t.Fatalf("payments was not reported as drifted: %+v", report.Drift)
		return
	}
	if payments.Actual != "Bob" {
		t.Errorf("actual owner = %q, want Bob, who does this work rather than all work", payments.Actual)
	}
}

// TestOwnershipSplitsTheThreeWaysAnOwnerCanDrift checks the report separates
// what a reader has to do something different about: an owner who has gone,
// one who owns an area on paper only, and one who is simply out-worked in their
// own area.
func TestOwnershipSplitsTheThreeWaysAnOwnerCanDrift(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		// Declared owners, from a source of record. This assigns them their
		// areas without any of them having done a thing.
		{Kind: connector.KindPerson, Name: "Gone", Email: "gone@x.com",
			Topics: []string{"alpha"}, Source: "codeowners"},
		{Kind: connector.KindPerson, Name: "Elsewhere", Email: "elsewhere@x.com",
			Topics: []string{"beta"}, Source: "codeowners"},
		{Kind: connector.KindPerson, Name: "Trailing", Email: "trailing@x.com",
			Topics: []string{"gamma"}, Source: "codeowners"},
		// Elsewhere works, just never on what they own.
		{Kind: connector.KindPerson, Name: "Elsewhere", Email: "elsewhere@x.com",
			Topics: []string{"delta", "delta"}, Source: "git"},
		// Trailing does work on their own area, but less than Rival.
		{Kind: connector.KindPerson, Name: "Trailing", Email: "trailing@x.com",
			Topics: []string{"gamma"}, Source: "git"},
		// The people actually doing each area.
		{Kind: connector.KindPerson, Name: "Rival", Email: "rival@x.com",
			Topics: []string{"alpha", "alpha", "beta", "beta", "gamma", "gamma", "gamma"}, Source: "git"},
	})
	ix.Canonicalize()

	report := Ownership(ix)
	if report.Silent != 1 || report.Unworked != 1 || report.Trailing != 1 {
		t.Errorf("split = silent %d, unworked %d, trailing %d; want one of each (drift %+v)",
			report.Silent, report.Unworked, report.Trailing, report.Drift)
	}
	if report.Silent+report.Unworked+report.Trailing != len(report.Drift) {
		t.Errorf("the three buckets total %d but %d areas drifted",
			report.Silent+report.Unworked+report.Trailing, len(report.Drift))
	}
}

// TestDriftNeedsTheChallengerToOutworkTheOwner covers what the finding claims.
// Saying an area has moved is saying the person written down as its owner is no
// longer the one doing the job, and nobody has done that while doing less of
// the work in it than the owner does.
//
// Ranking everybody and calling the winner the owner reported drift on a coin
// toss: on home-assistant/core it named a challenger with a ninth of the
// owner's work in the area. Ratios of confirmed findings ran 0.93 to 2.32,
// confirmed bogus ones 0.11 to 0.40.
func TestDriftNeedsTheChallengerToOutworkTheOwner(t *testing.T) {
	t.Parallel()

	build := func(t *testing.T, ownerWork, challengerWork int) OwnershipReport {
		t.Helper()
		ix := index.New()
		recs := []connector.Record{
			// Declared on paper, which is weight without work.
			{Kind: connector.KindPerson, Name: "Owner Of Record", Email: "owner@x.com",
				Topics: []string{"billing-retries"}, Source: "codeowners"},
		}
		for i := 0; i < ownerWork; i++ {
			recs = append(recs, connector.Record{Kind: connector.KindPerson,
				Name: "Owner Of Record", Email: "owner@x.com",
				Topics: []string{"billing-retries"}, Source: "git"})
		}
		for i := 0; i < challengerWork; i++ {
			recs = append(recs, connector.Record{Kind: connector.KindPerson,
				Name: "Challenger", Email: "challenger@x.com",
				Topics: []string{"billing-retries"}, Source: "git"})
		}
		// The owner also works widely elsewhere, and the challenger does not.
		// Without this the guard never fires: the ranking discounts weight by
		// everything a person does, so a narrow challenger only overtakes a
		// broad owner when the owner has a career behind them. Leaving it out
		// made an earlier version of this test pass with the guard deleted.
		for i := 0; i < 400; i++ {
			recs = append(recs, connector.Record{Kind: connector.KindPerson,
				Name: "Owner Of Record", Email: "owner@x.com",
				Topics: []string{fmt.Sprintf("other-area-%d", i)}, Source: "git"})
		}
		recs = append(recs, connector.Record{Kind: connector.KindPerson,
			Name: "Someone Else", Email: "else@x.com",
			Topics: []string{"search-indexing", "search-indexing"}, Source: "git"})
		ix.Build(recs)
		ix.AutoJoin()
		ix.Canonicalize()
		return Ownership(ix)
	}

	// Doing a fraction of the owner's work is not displacing them, however the
	// ranking discounts a broad career.
	if r := build(t, 9, 1); r.Drifted() != 0 {
		t.Errorf("drift reported where the challenger does a ninth of the work: %+v", r.Drift)
	}
	// Clearly out-working the owner in their own area is exactly the finding.
	if r := build(t, 2, 9); r.Drifted() != 1 {
		t.Errorf("no drift reported where the challenger does four times the work: %+v", r.Drift)
	}
	// An owner who has never touched the area is displaced by anyone, and needs
	// no margin: there is nothing to hold.
	if r := build(t, 0, 3); r.Drifted() != 1 {
		t.Errorf("paper-only ownership was not reported as drift: %+v", r.Drift)
	}
}

// TestOwnershipSetsGroupOwnedAreasAside covers ownership that names nobody: an
// area owned only by a squad handle or a bot account has no person to have
// held it, so it is counted as group-owned, never listed as drift, and left
// out of the drift share's denominator. A mixed area with one human owner is
// still judged.
func TestOwnershipSetsGroupOwnedAreasAside(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		// A squad owns alerting; only a bot owns tooling; Dana co-owns billing
		// with a squad and somebody else does the billing work.
		{Kind: connector.KindPerson, Name: "@grafana/alerting-squad",
			PersonID: "codeowners:grafana/alerting-squad",
			Topics:   []string{"alerting"}, Source: "codeowners", Weight: 1},
		{Kind: connector.KindPerson, Name: "@grafanabot",
			PersonID: "codeowners:grafanabot",
			Topics:   []string{"tooling"}, Source: "codeowners", Weight: 1},
		{Kind: connector.KindPerson, Name: "@grafana/payments-squad",
			PersonID: "codeowners:grafana/payments-squad",
			Topics:   []string{"billing"}, Source: "codeowners", Weight: 1},
		{Kind: connector.KindPerson, Name: "Dana", Email: "dana@x.com",
			Topics: []string{"billing"}, Source: "codeowners"},
		{Kind: connector.KindPerson, Name: "Eve", Email: "eve@x.com",
			Topics: []string{"billing", "billing", "billing", "alerting", "tooling"}, Source: "slack"},
	})
	ix.Canonicalize()

	report := Ownership(ix)
	if report.GroupOwned != 2 {
		t.Errorf("groupOwned = %d, want alerting and tooling set aside", report.GroupOwned)
	}
	for _, d := range report.Drift {
		if d.Topic == "alerting" || d.Topic == "tooling" {
			t.Errorf("group-owned area %q listed as drift", d.Topic)
		}
	}
	// The share judges only the judgeable: one billing area, drifted to Eve.
	if got := report.Share(); got != 1 {
		t.Errorf("share = %.2f, want 1.00 over the one judged area", got)
	}
}

// TestSquadAreasJudgedThroughTheOrgChart covers the bridge between the two
// team notions: a CODEOWNERS squad the org chart also knows as a team is
// judged through its members. A member leading the area holds it, an outsider
// leading it is drift, and a squad matching no team stays set aside.
func TestSquadAreasJudgedThroughTheOrgChart(t *testing.T) {
	t.Parallel()
	base := []connector.Record{
		// The org chart names the Alerting team and its two members.
		{Kind: connector.KindPerson, Name: "Mia Torres", Email: "mia@x.com",
			Team: "Alerting", Source: "org-csv"},
		{Kind: connector.KindPerson, Name: "Raj Patel", Email: "raj@x.com",
			Team: "Alerting", Source: "org-csv"},
		// An outsider on another team.
		{Kind: connector.KindPerson, Name: "Zoe Lang", Email: "zoe@x.com",
			Team: "Payments", Source: "org-csv"},
		// CODEOWNERS declares the squad over silences, and an unmatched squad
		// over paging.
		{Kind: connector.KindPerson, Name: "@grafana/alerting-squad",
			PersonID: "codeowners:grafana/alerting-squad",
			Topics:   []string{"silences"}, Source: "codeowners", Weight: 1},
		{Kind: connector.KindPerson, Name: "@grafana/mystery-squad",
			PersonID: "codeowners:grafana/mystery-squad",
			Topics:   []string{"paging"}, Source: "codeowners", Weight: 1},
	}
	work := func(email string, n int, topic string) connector.Record {
		topics := make([]string, n)
		for i := range topics {
			topics[i] = topic
		}
		return connector.Record{Kind: connector.KindPerson, Email: email,
			Topics: topics, Source: "git"}
	}

	// Test 0: A member leads the squad's area: held, not drift, not group.
	ixa := index.New()
	ixa.Build(append(append([]connector.Record{}, base...), work("mia@x.com", 4, "silences"), work("zoe@x.com", 8, "paging")))
	ixa.AutoJoin()
	ixa.Canonicalize()
	rep := Ownership(ixa)
	for _, d := range rep.Drift {
		if d.Topic == "silences" {
			t.Errorf("member-led squad area reported as drift: %+v", d)
		}
	}
	if rep.GroupOwned != 1 {
		t.Errorf("groupOwned = %d, want only the unmatched mystery squad", rep.GroupOwned)
	}

	// Test 1: An outsider leads the squad's area: drift, judged.
	ixb := index.New()
	ixb.Build(append(append([]connector.Record{}, base...), work("zoe@x.com", 6, "silences"), work("mia@x.com", 1, "silences")))
	ixb.AutoJoin()
	ixb.Canonicalize()
	rep = Ownership(ixb)
	found := false
	for _, d := range rep.Drift {
		if d.Topic == "silences" && d.Actual == "Zoe Lang" {
			found = true
		}
	}
	if !found {
		t.Errorf("outsider-led squad area not reported as drift: %+v", rep.Drift)
	}
}
