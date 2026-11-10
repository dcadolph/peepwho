package resolve

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// decayedIndex builds a graph whose weights are fractional, which is what the
// product's own are: recency decay multiplies a count by a half-life curve, so
// a subject's share is an irrational-looking float and the order it is summed
// in decides its last bit. Built from whole numbers the sums are exact however
// they are ordered, and a determinism test over them proves nothing.
func decayedIndex(t *testing.T, people, spread int) *index.Index {
	t.Helper()
	ix := index.New()
	ix.SetHalfLife(90 * 24 * time.Hour)
	now := time.Now()
	var recs []connector.Record
	for i := range people {
		// Subjects repeat unevenly, so one person's own subjects carry
		// different weights. Given each person a single timestamp, an even
		// spread would decay every one of their subjects by the same factor,
		// their career total would be a sum of equal terms, and it would come
		// out identical whatever order it was taken in. A test built that way
		// passes with the bug present.
		var topics []string
		for k, base := range []string{"billing", "ledger", "invoices", "dunning", "kafka"} {
			for range 1 + (i+k)%5 {
				topics = append(topics, base)
			}
		}
		for j := range 7 {
			for range 1 + (i*j)%3 {
				topics = append(topics, fmt.Sprintf("area-%d", (i+j)%spread))
			}
		}
		recs = append(recs, connector.Record{
			Kind: connector.KindPerson, Name: fmt.Sprintf("P%02d Last", i),
			Email: fmt.Sprintf("p%02d@corp.com", i), Topics: topics, Source: "git",
			// Staggered so every person decays by a different factor.
			Time: now.AddDate(0, 0, -(i*7 + 3)),
		})
	}
	ix.Build(recs)
	ix.AutoJoin()
	ix.Canonicalize()
	return ix
}

// TestRiskIsByteDeterministic pins the property the sealed deliverable rests
// on: two runs over one unchanged index produce the same bytes.
//
// Go randomizes map iteration for every range statement, and float addition is
// not associative, so a share summed as the map happened to yield it differs
// in its last bit between runs. Measured on a real repository that moved about
// a thousand values in findings.json. Nothing visible changed, but a share
// feeds a sort comparison, so a tie can break differently, and an assessment a
// client cannot reproduce byte for byte is not what this product claims to be.
//
// The seal covers findings.json, so this is also what lets two people run the
// same assessment and compare digests.
func TestRiskIsByteDeterministic(t *testing.T) {
	t.Parallel()
	// Many people sharing each subject, so a per-subject sum has many terms
	// and the order it is taken in can move the result.
	ix := decayedIndex(t, 60, 23)

	first, err := json.Marshal(Risk(ix, 0))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for run := range 30 {
		again, err := json.Marshal(Risk(ix, 0))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("run %d differs from the first over an unchanged index", run)
		}
	}
}

// leaderPrint renders a lead ranking including its scores, in subject order.
func leaderPrint(ranked map[model.ID][]leader) string {
	ids := make([]model.ID, 0, len(ranked))
	for tid := range ranked {
		ids = append(ids, tid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var b strings.Builder
	for _, tid := range ids {
		fmt.Fprintf(&b, "%s:", tid)
		for _, l := range ranked[tid] {
			fmt.Fprintf(&b, " %s=%v", l.id, l.score)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// TestLeadersAreByteDeterministic pins the same property for the lead score,
// whose divisor is a career total summed over the same randomized maps and
// which decides who a subject is said to rest on.
func TestLeadersAreByteDeterministic(t *testing.T) {
	t.Parallel()
	ix := decayedIndex(t, 40, 17)

	// The scores are compared, not only the winner. A career total differing
	// in its last bit usually leaves the same person on top, so comparing
	// names alone passes whatever the summation order is and guards nothing.
	// The score is what a tie breaks on.
	first := leaderPrint(Leaders(ix, 3))
	for run := range 30 {
		if again := leaderPrint(Leaders(ix, 3)); again != first {
			t.Fatalf("run %d scored leaders differently over an unchanged index", run)
		}
	}
}

// TestPlaceLeadsIsByteDeterministic pins the ranking every system finding is
// ordered by.
//
// One person holds several tally keys, so their discounted work is a sum of
// quotients that are not exactly representable. Accumulated in Go's randomized
// map order the last bits differ between runs, and two people whose work is
// inexactly equal break their tie differently: measured on kubernetes that
// moved a scored directory. The name tie-break only settles exactly equal
// work, so it cannot cover this.
func TestPlaceLeadsIsByteDeterministic(t *testing.T) {
	t.Parallel()
	ix := index.New()
	var recs []connector.Record
	dirWork := map[string]map[string]float64{"internal/billing": {}}
	totals := map[string]float64{}
	for i := range 24 {
		email := fmt.Sprintf("p%02d@corp.com", i)
		recs = append(recs, connector.Record{
			Kind: connector.KindPerson, Name: fmt.Sprintf("P%02d Last", i),
			Email: email, Topics: []string{"billing"}, Source: "git",
		})
		// Several addresses per person, so one holder's work is a sum of
		// several quotients rather than a single one.
		for k := range 3 {
			key := fmt.Sprintf("p%02d+%d@corp.com", i, k)
			dirWork["internal/billing"][key] = float64(7+i+k) / 3.0
			totals[key] = float64(90 + i*11 + k*7)
		}
	}
	ix.Build(recs)
	ix.AutoJoin()
	ix.Canonicalize()

	first, err := json.Marshal(PlaceLeads(ix, dirWork, totals, 1, 5))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for run := range 30 {
		again, err := json.Marshal(PlaceLeads(ix, dirWork, totals, 1, 5))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("run %d ranked the place differently over an unchanged tally", run)
		}
	}
}
