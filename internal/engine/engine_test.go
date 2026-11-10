package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/policy"
	"github.com/dcadolph/whotoask/internal/recall"
	"github.com/dcadolph/whotoask/internal/simorg"
)

// TestNewPanicsOnNilIndex verifies a nil index is refused at construction.
func TestNewPanicsOnNilIndex(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("New(nil) did not panic")
		}
	}()
	New(nil, policy.Default())
}

// TestEngineAsk verifies keyword answers over the simulated company and that
// a bad resolver choice fails before any ranking.
func TestEngineAsk(t *testing.T) {
	t.Parallel()
	eng := New(newSimIndex(t), policy.Default())
	tests := []struct {
		Want          error
		Cfg           ResolverConfig
		Query         string
		WantFirstID   string
		WantNoResults bool
	}{{ // Test 0: Keyword answer names the billing owner first.
		Query: "billing retries", WantFirstID: "angela@example.com",
	}, { // Test 1: A query no one matches.
		Query: "zzqx nothing", WantNoResults: true,
	}, { // Test 2: Unknown mode.
		Query: "billing", Cfg: ResolverConfig{Mode: "bogus"}, Want: ErrBadArgs,
	}, { // Test 3: Strict denies the cloud.
		Query: "billing", Cfg: ResolverConfig{Mode: "llm", Provider: "anthropic"},
		Want: policy.ErrEgressDenied,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ans, err := eng.Ask(context.Background(), test.Query, 5, test.Cfg)
			if !errors.Is(err, test.Want) {
				t.Fatalf("Ask error = %v, want %v", err, test.Want)
			}
			if test.Want != nil {
				return
			}
			if test.WantNoResults {
				if len(ans.People) != 0 {
					t.Errorf("Ask(%q) people = %d, want 0", test.Query, len(ans.People))
				}
				return
			}
			if len(ans.People) == 0 {
				t.Fatalf("Ask(%q) returned no people", test.Query)
			}
			if diff := cmp.Diff(test.WantFirstID, string(ans.People[0].Person.ID)); diff != "" {
				t.Errorf("first person mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestEngineSetFeedbackStrength verifies every preset is accepted and an
// unknown one is a bad argument.
func TestEngineSetFeedbackStrength(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Want error
		Name string
	}{
		{Name: ""},                       // Test 0: Empty means normal.
		{Name: "off"},                    // Test 1: Off.
		{Name: "low"},                    // Test 2: Low.
		{Name: "normal"},                 // Test 3: Normal.
		{Name: "high"},                   // Test 4: High.
		{Name: "wild", Want: ErrBadArgs}, // Test 5: Unknown preset.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			eng := New(index.New(), policy.Default())
			if err := eng.SetFeedbackStrength(test.Name); !errors.Is(err, test.Want) {
				t.Errorf("SetFeedbackStrength(%q) = %v, want %v", test.Name, err, test.Want)
			}
		})
	}
}

// TestEngineSearchAndPerson verifies lookups by name and by canonical id.
func TestEngineSearchAndPerson(t *testing.T) {
	t.Parallel()
	eng := New(newSimIndex(t), policy.Default())
	tests := []struct {
		ID       string
		WantName string
		WantOK   bool
	}{{ // Test 0: Known person.
		ID: "angela@example.com", WantName: "Angela Malone", WantOK: true,
	}, { // Test 1: Unknown person.
		ID: "nobody@nowhere.example",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			p, ok := eng.Person(test.ID)
			if ok != test.WantOK {
				t.Fatalf("Person(%q) ok = %t, want %t", test.ID, ok, test.WantOK)
			}
			if diff := cmp.Diff(test.WantName, p.Name); diff != "" {
				t.Errorf("name mismatch (-want +got):\n%s", diff)
			}
		})
	}
	hits := eng.Search("angela", 5)
	if len(hits) == 0 || hits[0].ID != "angela@example.com" {
		t.Errorf("Search(angela) = %+v, want angela@example.com first", hits)
	}
}

// TestEngineStatus verifies counts and that age is measured from the caller's
// clock, not the process clock.
func TestEngineStatus(t *testing.T) {
	t.Parallel()
	dir := Dir(t.TempDir())
	if err := NewStore(dir, nil).SaveIndex(newSimIndex(t)); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	ix, err := NewStore(dir, nil).LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	eng := New(ix, policy.Default())
	built := ix.BuiltAt()
	if built.IsZero() {
		t.Fatal("loaded index has no build time")
	}
	st := eng.Status(built.Add(90 * time.Minute))
	if diff := cmp.Diff(90*time.Minute, st.Age); diff != "" {
		t.Errorf("age mismatch (-want +got):\n%s", diff)
	}
	if st.People != len(ix.Graph.People) || st.People == 0 {
		t.Errorf("people = %d, want %d and nonzero", st.People, len(ix.Graph.People))
	}
	if st.Subjects+st.Words != len(ix.Graph.Topics) {
		t.Errorf("subjects %d + words %d != topics %d", st.Subjects, st.Words, len(ix.Graph.Topics))
	}
	if len(st.Sources) == 0 {
		t.Error("status lists no sources")
	}

	fresh := New(index.New(), policy.Default()).Status(time.Unix(0, 0))
	if !fresh.BuiltAt.IsZero() || fresh.Age != 0 {
		t.Errorf("unsaved index status = %+v, want zero build time and age", fresh)
	}
}

// TestEngineRecaller verifies recall is scoped to the named person.
func TestEngineRecaller(t *testing.T) {
	t.Parallel()
	ix := newSimIndex(t)
	eps, err := simorg.BuildEpisodes(ix)
	if err != nil {
		t.Fatalf("simorg.BuildEpisodes: %v", err)
	}
	res := New(ix, policy.Default()).Recaller(eps)
	who := res.Who("angela@example.com")
	if who == "" {
		t.Fatal("Recaller does not know angela@example.com")
	}
	ans := res.Resolve(context.Background(), recall.Query{Text: "billing retries", Person: who, Limit: 5})
	if len(ans.Episodes) == 0 {
		t.Error("recall found no conversation for the demo person")
	}
	stranger := res.Who("nobody@nowhere.example")
	if res.Known(stranger) {
		t.Errorf("Recaller knows %q, who took part in no conversation", stranger)
	}
	other := res.Resolve(context.Background(),
		recall.Query{Text: "billing retries", Person: stranger, Limit: 5})
	if len(other.Episodes) != 0 {
		t.Errorf("recall for a stranger returned %d conversations, want 0", len(other.Episodes))
	}
}
