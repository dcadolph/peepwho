package resolve

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// successorIx builds a graph with a sole holder, a near colleague, a distant
// one, and a reviewer who writes nothing.
func successorIx(t *testing.T) *index.Index {
	t.Helper()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Holder", Email: "holder@x.com",
			Topics: []string{"billing", "ledger", "invoices", "dunning"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Near", Email: "near@x.com",
			Topics: []string{"billing", "ledger", "invoices"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Far", Email: "far@x.com",
			Topics: []string{"kafka", "streams", "billing"}, Source: "git"},
	})
	ix.Canonicalize()
	return ix
}

// TestSuccessorForPrefersTheAdjacentColleague verifies the person sharing most
// of the holder's subjects is named ahead of one sharing a single word, and
// that the reasons say why in printable prose.
func TestSuccessorForPrefersTheAdjacentColleague(t *testing.T) {
	t.Parallel()
	ix := successorIx(t)
	in := NewSuccessorInputs(ix, nil, nil)
	got, ok := in.SuccessorFor(ix, "holder@x.com", "")
	if !ok {
		t.Fatal("no successor named, want the adjacent colleague")
	}
	if got.Name != "Near" {
		t.Errorf("successor = %q, want Near: three shared subjects beat one", got.Name)
	}
	if len(got.Reasons) == 0 || !strings.Contains(got.Reasons[0].Detail, "3 of the 4") {
		t.Errorf("reasons = %+v, want the overlap counted as 3 of the 4", got.Reasons)
	}
}

// TestSuccessorForPlaceEvidenceOutranksOverlap verifies somebody already
// working in the holder's directory is named over a wordier overlap, because
// working there is the strongest preparation the record can show.
func TestSuccessorForPlaceEvidenceOutranksOverlap(t *testing.T) {
	t.Parallel()
	ix := successorIx(t)
	places := []Place{{
		Dir: "internal/billing",
		Holders: []PlaceHolder{
			{ID: "holder@x.com", Name: "Holder", Work: 40},
			{ID: "far@x.com", Name: "Far", Work: 6},
		},
	}}
	in := NewSuccessorInputs(ix, places, nil)
	got, ok := in.SuccessorFor(ix, "holder@x.com", "internal/billing")
	if !ok {
		t.Fatal("no successor named")
	}
	if got.Name != "Far" {
		t.Errorf("successor = %q, want Far: real work in the place beats overlap", got.Name)
	}
	found := false
	for _, r := range got.Reasons {
		if r.Kind == "place" && strings.Contains(r.Detail, "internal/billing") {
			found = true
		}
	}
	if !found {
		t.Errorf("reasons = %+v, want the place named", got.Reasons)
	}
}

// TestSuccessorForReviewerCounts verifies a person who only reviews still
// gathers evidence, which is what surfaces the approver who writes nothing.
func TestSuccessorForReviewerCounts(t *testing.T) {
	t.Parallel()
	ix := successorIx(t)
	reviewers := map[string]map[model.ID]bool{
		"internal/billing": {"far@x.com": true},
	}
	in := NewSuccessorInputs(ix, nil, reviewers)
	got, ok := in.SuccessorFor(ix, "holder@x.com", "internal/billing")
	if !ok {
		t.Fatal("no successor named")
	}
	if got.Name != "Far" {
		t.Errorf("successor = %q, want the reviewer Far", got.Name)
	}
}

// TestSuccessorForNobodyClose verifies the floor: when nobody shares anything,
// the answer is false rather than the least-bad stranger, because naming a
// person on no evidence is the failure this feature must never have.
func TestSuccessorForNobodyClose(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Alone", Email: "alone@x.com",
			Topics: []string{"billing", "ledger"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Stranger", Email: "s@x.com",
			Topics: []string{"kafka"}, Source: "git"},
	})
	ix.Canonicalize()
	in := NewSuccessorInputs(ix, nil, nil)
	if got, ok := in.SuccessorFor(ix, "alone@x.com", ""); ok {
		t.Errorf("named %q on no shared evidence, want nobody", got.Name)
	}
}

// TestSuccessorForNeverNamesTheHolder verifies the holder cannot be their own
// successor however the evidence lands.
func TestSuccessorForNeverNamesTheHolder(t *testing.T) {
	t.Parallel()
	ix := successorIx(t)
	places := []Place{{
		Dir:     "internal/billing",
		Holders: []PlaceHolder{{ID: "holder@x.com", Name: "Holder", Work: 40}},
	}}
	in := NewSuccessorInputs(ix, places, nil)
	got, ok := in.SuccessorFor(ix, "holder@x.com", "internal/billing")
	if ok && got.ID == "holder@x.com" {
		t.Error("the holder was named as their own successor")
	}
}

// crowd returns n candidate ids that sort ahead of any ordinary address, so a
// test can put the person who should win at the far end of the alphabet.
func crowd(n int) []model.ID {
	ids := make([]model.ID, 0, n)
	for i := range n {
		ids = append(ids, model.ID(fmt.Sprintf("a%02d@x.com", i)))
	}
	return ids
}

// TestSuccessorForScoresEveryCandidate checks nobody is dropped before
// scoring. Before this, the candidates were sorted by id and cut to the first
// 64, so in a busy area the colleague with the most work in the place was
// never scored when their address sorted late, and a weaker name from early in
// the alphabet was reported as the nearest candidate.
func TestSuccessorForScoresEveryCandidate(t *testing.T) {
	t.Parallel()
	const (
		holder = model.ID("holder@x.com")
		late   = model.ID("zoe@x.com")
		dir    = "internal/billing"
	)
	held := []string{"billing", "dunning", "invoices", "ledger", "refunds"}
	// inputs builds the evidence for one case: the crowd shares the first
	// three of the holder's subjects, and the late candidate shares lateShares
	// of them, has lateWork in the place, and reviewed it when lateReview is
	// set. crowdWork is the work each crowd member has in the place.
	inputs := func(n, lateShares int, crowdWork, lateWork float64, lateReview bool) *successorInputs {
		in := &successorInputs{
			holders:      make(map[string]map[model.ID]bool),
			personTopics: make(map[model.ID]map[string]bool),
			placeWork:    map[string]map[model.ID]float64{dir: {holder: 40}},
			reviewed:     map[string]map[model.ID]bool{dir: {}},
		}
		hold := func(id model.ID, topics []string) {
			for _, topic := range topics {
				if in.holders[topic] == nil {
					in.holders[topic] = make(map[model.ID]bool)
				}
				in.holders[topic][id] = true
				if in.personTopics[id] == nil {
					in.personTopics[id] = make(map[string]bool)
				}
				in.personTopics[id][topic] = true
			}
		}
		hold(holder, held)
		for _, id := range crowd(n) {
			hold(id, held[:3])
			if crowdWork > 0 {
				in.placeWork[dir][id] = crowdWork
			}
		}
		hold(late, held[:lateShares])
		if lateWork > 0 {
			in.placeWork[dir][late] = lateWork
		}
		if lateReview {
			in.reviewed[dir][late] = true
		}
		return in
	}

	tests := []struct {
		Crowd      int
		LateShares int
		CrowdWork  float64
		LateWork   float64
		LateReview bool
		WantID     string
	}{{ // Test 0: The heaviest committer sorts after 70 light ones.
		Crowd: 70, CrowdWork: 1, LateWork: 30, WantID: string(late),
	}, { // Test 1: The widest overlap sorts after 70 narrower ones.
		Crowd: 70, LateShares: 5, WantID: string(late),
	}, { // Test 2: The only reviewer sorts after 70 people with overlap alone.
		Crowd: 70, LateReview: true, WantID: string(late),
	}, { // Test 3: An even crowd of 70 still breaks to the lowest id.
		Crowd: 70, LateShares: 3, WantID: "a00@x.com",
	}, { // Test 4: A small crowd is scored as before.
		Crowd: 5, CrowdWork: 1, LateWork: 30, WantID: string(late),
	}, { // Test 5: A late candidate with less work does not win on position.
		Crowd: 70, CrowdWork: 5, LateWork: 1, WantID: "a00@x.com",
	}}
	ix := index.New()
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			in := inputs(
				test.Crowd, test.LateShares, test.CrowdWork, test.LateWork, test.LateReview,
			)
			got, ok := in.SuccessorFor(ix, holder, dir)
			if !ok {
				t.Fatal("no successor named")
			}
			if diff := cmp.Diff(test.WantID, got.ID); diff != "" {
				t.Errorf("successor mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
