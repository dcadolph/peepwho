package ownersbench

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/model"
)

// changeEp builds a merged-change episode for one pull request.
func changeEp(repo string, number int, asked string, others ...string) episode.Episode {
	people := make([]string, 0, len(others)+1)
	if asked != "" {
		people = append(people, asked)
	}
	people = append(people, others...)
	ep := episode.Episode{
		ID:    fmt.Sprintf("github:%s:%d", repo, number),
		Kind:  episode.KindChange,
		Place: repo,
	}
	for _, login := range people {
		ep.Participants = append(ep.Participants, model.ID("github:"+login))
	}
	if asked != "" {
		ep.Asked = model.ID("github:" + asked)
	}
	return ep
}

func TestEpisodeRef(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name    string
		In      episode.Episode
		WantRef connector.PullRef
		WantOK  bool
	}{{ // Test 0: A change episode yields its repository and number.
		Name:    "change",
		In:      changeEp("kubernetes/test-infra", 41, "ann", "bo"),
		WantRef: connector.PullRef{Repo: "kubernetes/test-infra", Number: 41},
		WantOK:  true,
	}, { // Test 1: A mixed-case place is lowered, so it keys like PullDirs.
		Name:    "lowered",
		In:      changeEp("Kubernetes/Test-Infra", 41, "ann", "bo"),
		WantRef: connector.PullRef{Repo: "kubernetes/test-infra", Number: 41},
		WantOK:  true,
	}, { // Test 2: A thread episode is not a pull request.
		Name:   "not a change",
		In:     episode.Episode{ID: "slack:C1:9", Kind: episode.KindThread, Place: "#ops"},
		WantOK: false,
	}, { // Test 3: An identifier with no number cannot be placed.
		Name:   "no number",
		In:     episode.Episode{ID: "github:acme/pay:tip", Kind: episode.KindChange, Place: "acme/pay"},
		WantOK: false,
	}, { // Test 4: A change episode with no place names no repository.
		Name:   "no place",
		In:     episode.Episode{ID: "github:acme/pay:7", Kind: episode.KindChange},
		WantOK: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			ep := test.In
			got, ok := episodeRef(&ep)
			if ok != test.WantOK {
				t.Fatalf("ok = %v, want %v", ok, test.WantOK)
			}
			if !ok {
				return
			}
			if diff := cmp.Diff(test.WantRef, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAnsweredOnly(t *testing.T) {
	t.Parallel()
	ref := func(n int) connector.PullRef {
		return connector.PullRef{Repo: "acme/pay", Number: n}
	}
	tests := []struct {
		Name   string
		People map[connector.PullRef][]string
		Eps    []episode.Episode
		Want   map[connector.PullRef][]string
	}{{ // Test 0: The author of the pull request is dropped, the rest stay.
		Name:   "asker dropped",
		People: map[connector.PullRef][]string{ref(1): {"ann", "bo", "cy"}},
		Eps:    []episode.Episode{changeEp("acme/pay", 1, "ann", "bo", "cy")},
		Want:   map[connector.PullRef][]string{ref(1): {"bo", "cy"}},
	}, { // Test 1: Case in the participant list does not save the asker.
		Name:   "asker dropped case-insensitively",
		People: map[connector.PullRef][]string{ref(1): {"Ann", "Bo"}},
		Eps:    []episode.Episode{changeEp("acme/pay", 1, "ann", "bo")},
		Want:   map[connector.PullRef][]string{ref(1): {"Bo"}},
	}, { // Test 2: A pull request with no episode keeps everybody.
		Name:   "no episode",
		People: map[connector.PullRef][]string{ref(2): {"ann", "bo"}},
		Eps:    []episode.Episode{changeEp("acme/pay", 1, "ann", "bo")},
		Want:   map[connector.PullRef][]string{ref(2): {"ann", "bo"}},
	}, { // Test 3: An episode that records no asker credits everyone present.
		Name:   "no asker",
		People: map[connector.PullRef][]string{ref(1): {"ann", "bo"}},
		Eps:    []episode.Episode{changeEp("acme/pay", 1, "", "ann", "bo")},
		Want:   map[connector.PullRef][]string{ref(1): {"ann", "bo"}},
	}, { // Test 4: An asker who drew no replies leaves the pull request empty.
		Name:   "asker alone",
		People: map[connector.PullRef][]string{ref(1): {"ann"}},
		Eps:    []episode.Episode{changeEp("acme/pay", 1, "ann")},
		Want:   map[connector.PullRef][]string{ref(1): {}},
	}, { // Test 5: Another repository's identically numbered pull is untouched.
		Name:   "other repo",
		People: map[connector.PullRef][]string{{Repo: "acme/web", Number: 1}: {"ann", "bo"}},
		Eps:    []episode.Episode{changeEp("acme/pay", 1, "ann", "bo")},
		Want:   map[connector.PullRef][]string{{Repo: "acme/web", Number: 1}: {"ann", "bo"}},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			got := answeredOnly(test.People, test.Eps)
			if diff := cmp.Diff(test.Want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// probeFixture is one repository's forge read: one pull request that changed
// two directories, whose author ann also committed there and whose reviewer bo
// never committed at all.
func probeFixture() Probe {
	return Probe{
		PullDirs:   map[string][]string{"acme/pay#1": {"pkg", "pkg/api"}},
		PullPeople: map[string][]string{"acme/pay#1": {"ann", "bo"}},
		Episodes:   []episode.Episode{changeEp("acme/pay", 1, "ann", "bo")},
	}
}

// baseFixture is the git tally the forge credit is folded onto, a fresh copy
// each call so one arm's credit never reaches the next.
func baseFixture() (map[string]map[string]float64, map[string]float64) {
	return map[string]map[string]float64{
		"pkg":     {"ann@x.com": 4},
		"pkg/api": {"ann@x.com": 4},
	}, map[string]float64{"ann@x.com": 8}
}

func TestProbeTally(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name    string
		Mode    CreditMode
		WantDir map[string]map[string]float64
		WantTot map[string]float64
		Want    error
	}{{ // Test 0: No credit leaves the git tally exactly as it was read.
		Name: "none", Mode: CreditNone,
		WantDir: map[string]map[string]float64{
			"pkg": {"ann@x.com": 4}, "pkg/api": {"ann@x.com": 4},
		},
		WantTot: map[string]float64{"ann@x.com": 8},
	}, { // Test 1: Crediting everyone gives the author review credit as well.
		Name: "all", Mode: CreditAll,
		WantDir: map[string]map[string]float64{
			"pkg":     {"ann@x.com": 4, "github:ann": 0.5, "github:bo": 0.5},
			"pkg/api": {"ann@x.com": 4, "github:ann": 0.5, "github:bo": 0.5},
		},
		WantTot: map[string]float64{"ann@x.com": 8, "github:ann": 0.5, "github:bo": 0.5},
	}, { // Test 2: Crediting answerers leaves the author out of both places.
		Name: "answered", Mode: CreditAnswered,
		WantDir: map[string]map[string]float64{
			"pkg":     {"ann@x.com": 4, "github:bo": 0.5},
			"pkg/api": {"ann@x.com": 4, "github:bo": 0.5},
		},
		WantTot: map[string]float64{"ann@x.com": 8, "github:bo": 0.5},
	}, { // Test 3: An unknown mode is refused rather than scored as something.
		Name: "bad mode", Mode: CreditMode("reviewers"), Want: ErrProbe,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			base, baseTotals := baseFixture()
			dirWork, totals, err := probeFixture().Tally(test.Mode, base, baseTotals)
			if !errors.Is(err, test.Want) {
				t.Fatalf("err = %v, want %v", err, test.Want)
			}
			if test.Want != nil {
				return
			}
			if diff := cmp.Diff(test.WantDir, dirWork, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("dirWork mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantTot, totals, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("totals mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestProbeTallyArmsAreIndependent checks that scoring one arm cannot change
// what the next one reads. AddReviewCredit folds credit into the maps it is
// handed, so a tally shared between arms would have the treatment scoring the
// control's credit on top of its own.
func TestProbeTallyArmsAreIndependent(t *testing.T) {
	t.Parallel()
	probe := probeFixture()
	base, baseTotals := baseFixture()
	if _, _, err := probe.Tally(CreditAll, base, baseTotals); err != nil {
		t.Fatalf("all: %v", err)
	}
	dirWork, totals, err := probe.Tally(CreditNone, base, baseTotals)
	if err != nil {
		t.Fatalf("none: %v", err)
	}
	want := map[string]map[string]float64{
		"pkg": {"ann@x.com": 4}, "pkg/api": {"ann@x.com": 4},
	}
	if diff := cmp.Diff(want, dirWork, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("dirWork mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]float64{"ann@x.com": 8}, totals, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("totals mismatch (-want +got):\n%s", diff)
	}
}

// TestProbeRoundTrip checks a saved probe scores the same as the one in memory,
// since every measured arm reads the file rather than the fetch.
func TestProbeRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "probe.json")
	if err := SaveProbe(path, probeFixture()); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := LoadProbe(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	base, baseTotals := baseFixture()
	wantDir, wantTot, err := probeFixture().Tally(CreditAnswered, base, baseTotals)
	if err != nil {
		t.Fatalf("memory: %v", err)
	}
	loadedBase, loadedTotals := baseFixture()
	gotDir, gotTot, err := loaded.Tally(CreditAnswered, loadedBase, loadedTotals)
	if err != nil {
		t.Fatalf("loaded: %v", err)
	}
	if diff := cmp.Diff(wantDir, gotDir, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("dirWork mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantTot, gotTot, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("totals mismatch (-want +got):\n%s", diff)
	}
}
