package simorg

import (
	"context"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// withoutAccountStatus rebuilds ix from its own source records with every
// account status unknown, so nobody is deactivated and the leaver ranks
// wherever her score puts her.
func withoutAccountStatus(t *testing.T, ix *index.Index) *index.Index {
	t.Helper()
	out := index.New()
	for _, name := range bigSourceOrder {
		recs := ix.SourceRecords(name)
		for i := range recs {
			recs[i].Account = model.AccountUnknown
		}
		out.Add(recs)
	}
	out.AutoJoin()
	out.Canonicalize()
	return out
}

// sampleLeaver returns the one deactivated person in the demo company.
func sampleLeaver(t *testing.T, ix *index.Index) *model.Person {
	t.Helper()
	var gone []*model.Person
	for _, p := range ix.Graph.People {
		if p.Deactivated() {
			gone = append(gone, p)
		}
	}
	if len(gone) != 1 {
		t.Fatalf("the demo company holds %d deactivated people, want 1", len(gone))
	}
	return gone[0]
}

// TestDepartedOnTheSample checks the demo company's departed kafka lag backup:
// she still holds the subject, never answers among the people, and is named
// in Departed below the owner.
func TestDepartedOnTheSample(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	leaver := sampleLeaver(t, ix)
	if leaver.Topics["kafka"] == 0 {
		t.Errorf("the leaver %s holds no kafka weight: %v", leaver.ID, leaver.Topics)
	}
	for _, limit := range []int{1, 5, 0} {
		ans, err := resolve.NewKeyword(ix).Resolve(context.Background(), "kafka lag", limit)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		// The owner is declared, so the answer is not padded with chatter; how
		// active people fill a limit is checked on the ranking itself below.
		if len(ans.People) == 0 || (limit > 0 && len(ans.People) > limit) {
			t.Errorf("limit %d: %d people, want at least the owner and at most the limit", limit, len(ans.People))
		}
		if slices.ContainsFunc(ans.People, func(m model.Match) bool { return m.Person.Deactivated() }) {
			t.Errorf("limit %d: a deactivated person answered among the people", limit)
		}
		if len(ans.Departed) == 0 || ans.Departed[0].Person.ID != leaver.ID {
			t.Errorf("limit %d: departed = %v, want %s first", limit, ans.Departed, leaver.ID)
		}
	}
}

// TestDepartedLeaveActiveRankingUnchanged checks that marking the leaver moves
// nobody else: for every question, the people are exactly the ranking of an
// index where nobody is deactivated, with the leaver taken out before the cut,
// in the same order and with the same scores and strengths.
func TestDepartedLeaveActiveRankingUnchanged(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	leaver := sampleLeaver(t, ix)
	plain := withoutAccountStatus(t, ix)
	if got, want := model.PeopleCount(ix.Graph.People), model.PeopleCount(plain.Graph.People); got != want {
		t.Errorf("people count = %d, want %d: the leaver must still count", got, want)
	}
	queries := []string{"kafka lag", "kafka", "contracts", "who owns contracts"}
	for id := range ix.Graph.Topics {
		queries = append(queries, string(id))
	}
	split := 0
	for _, q := range queries {
		all := plain.Search(q, 0)
		var stayed []model.Match
		leaverFound := false
		for _, m := range all {
			if m.Person.ID == leaver.ID {
				leaverFound = true
				continue
			}
			stayed = append(stayed, m)
		}
		for _, limit := range []int{5, 0} {
			people, departed := ix.SearchSplit(q, limit)
			want := resolve.Cap(stayed, limit)
			if diff := cmp.Diff(peopleKeys(want), peopleKeys(people), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("SearchSplit(%q, %d) people moved (-unmarked +marked):\n%s", q, limit, diff)
			}
			if got := len(departed) == 1 && departed[0].Person.ID == leaver.ID; got != leaverFound {
				t.Errorf("SearchSplit(%q, %d) departed = %v, want the leaver exactly when she matched", q, limit, departed)
			}
		}
		if at := slices.IndexFunc(all, func(m model.Match) bool { return m.Person.ID == leaver.ID }); at >= 0 && at < 5 {
			split++
		}
	}
	if split == 0 {
		t.Error("the leaver never ranked inside a limit of 5, so the split was never tested")
	}
}

// TestDepartedSampleRecordsCarryStatus checks the leaver's status arrives the
// way a real workspace sends it: on her Slack record, through the connector.
func TestDepartedSampleRecordsCarryStatus(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	gone := 0
	for _, rec := range ix.SourceRecords("slack") {
		if rec.Kind == connector.KindPerson && rec.Account == model.AccountDeactivated {
			gone++
		}
	}
	if gone != 1 {
		t.Errorf("slack person records marked deactivated = %d, want 1", gone)
	}
}
