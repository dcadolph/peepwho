package resolve

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// leaver is the email of the deactivated person in departedIndex.
const leaver = "lena@x.com"

// departedIndex builds an index of active people who know payments plus Lena
// Gone, who knows it best, with her account status set to lena.
func departedIndex(t *testing.T, active int, lena model.AccountStatus) *index.Index {
	t.Helper()
	recs := []connector.Record{{
		Source: "slack", PersonID: "slack:ugone", Name: "Lena Gone", Email: leaver,
		Title: "Engineer", Topics: []string{"payments", "payments", "payments"}, Weight: 3,
		Account: lena,
	}}
	for i := range active {
		recs = append(recs, connector.Record{
			Source: "slack", PersonID: fmt.Sprintf("slack:u%d", i), Name: fmt.Sprintf("Active %d", i),
			Email: fmt.Sprintf("a%d@x.com", i), Title: "Engineer", Topics: []string{"payments"},
			Weight: float64(active - i), Account: model.AccountActive,
		})
	}
	ix := index.New()
	ix.Build(recs)
	if got := ix.Graph.People[leaver].Deactivated(); got != (lena == model.AccountDeactivated) {
		t.Fatalf("fixture: %s deactivated = %v with status %v", leaver, got, lena)
	}
	return ix
}

// emails lists the matched people's emails in order.
func emails(ms []model.Match) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Person.Email)
	}
	return out
}

// referenceOrder is the keyword order of everyone when Lena's status is
// unknown, so she ranks wherever her score puts her.
func referenceOrder(t *testing.T, active int) []string {
	t.Helper()
	ans, err := NewKeyword(departedIndex(t, active, model.AccountUnknown)).
		Resolve(context.Background(), "payments", 0)
	if err != nil {
		t.Fatalf("reference resolve: %v", err)
	}
	return emails(ans.People)
}

// TestDepartedRankLast checks that every resolver fills People with the active
// people in the order they would have had anyway, never with a deactivated
// one, and keeps the deactivated person in Departed rather than dropping them.
func TestDepartedRankLast(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Resolver func(ix *index.Index) Resolver
		// ModelFirst is the person the model ranks first, moved to the front of
		// the expected people.
		ModelFirst string
		Active     int
		Limit      int
	}{{ // Test 0: Keyword, six active at limit 5.
		Resolver: func(ix *index.Index) Resolver { return NewKeyword(ix) },
		Active:   6, Limit: 5,
	}, { // Test 1: Keyword, three active at limit 5: the leaver never pads People.
		Resolver: func(ix *index.Index) Resolver { return NewKeyword(ix) },
		Active:   3, Limit: 5,
	}, { // Test 2: Keyword, no limit.
		Resolver: func(ix *index.Index) Resolver { return NewKeyword(ix) },
		Active:   2, Limit: 0,
	}, { // Test 3: Semantic, six active at limit 5.
		Resolver: func(ix *index.Index) Resolver { return NewSemantic(ix, fakeEmbedder{}) },
		Active:   6, Limit: 5,
	}, { // Test 4: LLM with an unusable reply keeps keyword order.
		Resolver: func(ix *index.Index) Resolver { return NewLLM(ix, &fakeChat{reply: "no"}, nil) },
		Active:   6, Limit: 5,
	}, { // Test 5: LLM naming the leaver first cannot put her in People.
		Resolver: func(ix *index.Index) Resolver {
			return NewLLM(ix, &fakeChat{reply: `{"people":["lena@x.com","a2@x.com"],"channels":[]}`}, nil)
		},
		ModelFirst: "a2@x.com",
		Active:     6, Limit: 5,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ref := referenceOrder(t, test.Active)
			if at := slices.Index(ref, leaver); at < 0 || at >= min(test.Active, 4) {
				t.Fatalf("fixture: %s ranks %d in %v, want inside the cut so the split is tested", leaver, at, ref)
			}
			want := slices.DeleteFunc(slices.Clone(ref), func(e string) bool { return e == leaver })
			if test.ModelFirst != "" {
				want = append([]string{test.ModelFirst},
					slices.DeleteFunc(want, func(e string) bool { return e == test.ModelFirst })...)
			}
			want = Cap(want, test.Limit)
			ans, err := test.Resolver(departedIndex(t, test.Active, model.AccountDeactivated)).
				Resolve(context.Background(), "payments", test.Limit)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if diff := cmp.Diff(want, emails(ans.People)); diff != "" {
				t.Errorf("people mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff([]string{leaver}, emails(ans.Departed), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("departed mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestDepartedNeverReachModel checks that the model is asked only about people
// who can still be reached.
func TestDepartedNeverReachModel(t *testing.T) {
	t.Parallel()
	chat := &fakeChat{reply: `{"people":[],"channels":[]}`}
	ix := departedIndex(t, 3, model.AccountDeactivated)
	if _, err := NewLLM(ix, chat, nil).Resolve(context.Background(), "payments", 5); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if strings.Contains(chat.gotUser, "Lena") || !strings.Contains(chat.gotUser, "Active 0") {
		t.Errorf("prompt = %q, want the active people and not the deactivated one", chat.gotUser)
	}
}

// TestDepartedSummary checks that the written recommendation names an active
// person when there is one and a deactivated one, marked, only when nobody
// active matched.
func TestDepartedSummary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantSummary string
		Active      int
	}{{ // Test 0: An active person matched, so the summary names them.
		Active: 1, WantSummary: "Talk to Active 0 (Engineer).",
	}, { // Test 1: Only the leaver matched, so she is named and marked.
		Active: 0, WantSummary: "Talk to Lena Gone (Engineer, deactivated).",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ix := departedIndex(t, test.Active, model.AccountDeactivated)
			ans, err := NewLLM(ix, &fakeChat{reply: "no"}, nil).Resolve(context.Background(), "payments", 5)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if diff := cmp.Diff(test.WantSummary, ans.Summary); diff != "" {
				t.Errorf("summary mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
