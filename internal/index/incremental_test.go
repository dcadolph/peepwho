package index

import (
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/model"
)

// hitRow is a compact, comparable view of one search result.
type hitRow struct {
	Email string
	Score float64
}

// rows reduces matches to comparable rows, rounding the score to absorb float
// noise so two indexes built different ways compare by the answer they give.
func rows(ms []model.Match) []hitRow {
	out := make([]hitRow, len(ms))
	for i, m := range ms {
		out[i] = hitRow{Email: m.Person.Email, Score: math.Round(m.Score*1e6) / 1e6}
	}
	return out
}

// TestMergeIncrementalMatchesFullBuild verifies that indexing a first batch and
// then folding in a later delta yields the same answers as a single full build
// over the combined activity. This is the correctness gate for incremental
// indexing: a partial re-read must never drop a person or a topic. Decay is
// disabled so the comparison is of accumulated affinity alone, and both indexes
// are reduced to stemmed terms so the term keys are produced the same way.
func TestMergeIncrementalMatchesFullBuild(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.AddDate(0, 1, 0)

	first := []connector.Record{
		{Source: "jira", Email: "jane@x.com", Name: "Jane", Topics: []string{"billing", "billing"},
			Text: "payment retries failing", Time: t1},
		{Source: "jira", Email: "bob@x.com", Name: "Bob", Topics: []string{"kafka"},
			Text: "consumer lag spikes", Time: t1},
	}
	delta := []connector.Record{
		{Source: "jira", Email: "jane@x.com", Name: "Jane", Topics: []string{"payments"},
			Text: "gateway timeout errors", Time: t2},
		{Source: "jira", Email: "carol@x.com", Name: "Carol", Topics: []string{"search"},
			Text: "index rebuild slow", Time: t2},
	}
	// The single full read a connector would emit: one record per person over all
	// activity, carrying the person's most recent time.
	full := []connector.Record{
		{Source: "jira", Email: "jane@x.com", Name: "Jane",
			Topics: []string{"billing", "billing", "payments"},
			Text:   "payment retries failing gateway timeout errors", Time: t2},
		{Source: "jira", Email: "bob@x.com", Name: "Bob", Topics: []string{"kafka"},
			Text: "consumer lag spikes", Time: t1},
		{Source: "jira", Email: "carol@x.com", Name: "Carol", Topics: []string{"search"},
			Text: "index rebuild slow", Time: t2},
	}

	ixFull := New()
	ixFull.SetHalfLife(-1)
	ixFull.Build(redactSlice(full))

	ixInc := New()
	ixInc.SetHalfLife(-1)
	ixInc.Build(first)
	ixInc.MergeIncremental(delta)

	// The fold keeps one record per identity, not first and delta stacked.
	if got := ixInc.SourceSize("jira"); got != 3 {
		t.Errorf("incremental source holds %d records, want 3 (one per person)", got)
	}

	for _, q := range []string{
		"billing", "payments", "kafka", "search", "retries", "gateway", "index rebuild", "consumer lag",
	} {
		if diff := cmp.Diff(rows(ixFull.Search(q, 5)), rows(ixInc.Search(q, 5))); diff != "" {
			t.Errorf("query %q: full vs incremental mismatch (-full +incremental):\n%s", q, diff)
		}
	}
}

// TestMergeIncrementalKeepsUnreadPeople verifies the defining property directly:
// a delta that mentions only one person leaves everyone else, and their topics,
// exactly as they were, rather than replacing the source's whole contribution.
func TestMergeIncrementalKeepsUnreadPeople(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.SetHalfLife(-1)
	ix.Build([]connector.Record{
		{Source: "jira", Email: "jane@x.com", Name: "Jane", Topics: []string{"billing"}, Text: "retries"},
		{Source: "jira", Email: "bob@x.com", Name: "Bob", Topics: []string{"kafka"}, Text: "lag"},
	})
	// Re-read returns only Jane, with new activity.
	ix.MergeIncremental([]connector.Record{
		{Source: "jira", Email: "jane@x.com", Name: "Jane", Topics: []string{"payments"}, Text: "gateway"},
	})

	// Bob and his topic survive the partial re-read.
	if hits := ix.Search("kafka", 3); len(hits) == 0 || hits[0].Person.Email != "bob@x.com" {
		t.Errorf("Bob was dropped by a delta that did not mention him: %+v", rows(hits))
	}
	// Jane still carries her original topic and the new one.
	if hits := ix.Search("billing", 3); len(hits) == 0 || hits[0].Person.Email != "jane@x.com" {
		t.Errorf("Jane lost her original topic after the merge: %+v", rows(hits))
	}
	if hits := ix.Search("payments", 3); len(hits) == 0 || hits[0].Person.Email != "jane@x.com" {
		t.Errorf("Jane did not gain the delta topic: %+v", rows(hits))
	}
}
