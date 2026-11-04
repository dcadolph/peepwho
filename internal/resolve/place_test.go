package resolve

import (
	"fmt"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
)

// TestPlaceLeadsDiscountsBreadth verifies the place model's whole reason to
// exist: a sweeper with more raw work in a directory ranks below the focused
// owner, identities fold to their canonical person, and quiet directories are
// dropped.
func TestPlaceLeadsDiscountsBreadth(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Owner", Email: "owner@x.com",
			Topics: []string{"billing"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Sweeper", Email: "sweep@x.com",
			Topics: []string{"billing"}, Source: "git"},
	})
	ix.Canonicalize()

	dirWork := map[string]map[string]float64{
		"billing": {"owner@x.com": 30, "sweep@x.com": 40},
		"quiet":   {"owner@x.com": 1},
	}
	totals := map[string]float64{"owner@x.com": 35, "sweep@x.com": 900}

	places := PlaceLeads(ix, dirWork, totals, 10, 3)
	if len(places) != 1 || places[0].Dir != "billing" {
		t.Fatalf("places = %+v, want billing alone; quiet is below the floor", places)
	}
	holders := places[0].Holders
	if len(holders) != 2 || holders[0].Name != "Owner" {
		t.Errorf("holders = %+v, want Owner first: 40 commits from someone with 900 "+
			"everywhere is less ownership than 30 from someone with 35", holders)
	}
	if holders[0].ID == "" {
		t.Error("holder has no canonical id")
	}
}

// TestFuseRanksKeepsOrphanSignal verifies a person only one ranking can see
// still surfaces, which is the property review-only approvers depend on.
func TestFuseRanksKeepsOrphanSignal(t *testing.T) {
	t.Parallel()
	got := FuseRanks(3,
		[]string{"CommitHeavy", "CommitMid", "CommitLow"},
		[]string{"ReviewOnly", "CommitHeavy"},
	)
	if got[0] != "CommitHeavy" {
		t.Errorf("fused[0] = %q, want the person both signals agree on", got[0])
	}
	found := false
	for _, n := range got {
		if n == "ReviewOnly" {
			found = true
		}
	}
	if !found {
		t.Errorf("fused = %v; the review-only person was crowded out", got)
	}
}

// TestAddReviewCredit verifies review participation lands on the places a
// pull request changed rather than on the words of its title, and that a
// reviewer who never commits can therefore hold a directory.
func TestAddReviewCredit(t *testing.T) {
	t.Parallel()
	dirWork := map[string]map[string]float64{
		"pkg/scheduler": {"author@x.com": 20},
	}
	totals := map[string]float64{"author@x.com": 400}
	acme := func(n int) connector.PullRef {
		return connector.PullRef{Repo: "acme/platform", Number: n}
	}
	pullDirs := map[connector.PullRef][]string{
		acme(7): {"pkg/scheduler"}, acme(9): {"pkg/other"},
	}
	pullPeople := map[connector.PullRef][]string{
		acme(7): {"reviewer-only"}, acme(8): {"nobody"},
	}

	AddReviewCredit(dirWork, totals, pullDirs, pullPeople)

	got := dirWork["pkg/scheduler"]["github:reviewer-only"]
	if got != reviewWeight {
		t.Errorf("scheduler review credit = %v, want %v", got, reviewWeight)
	}
	if totals["github:reviewer-only"] != reviewWeight {
		t.Errorf("reviewer breadth = %v, want it counted", totals["github:reviewer-only"])
	}
	if _, ok := dirWork["pkg/other"]["github:nobody"]; ok {
		t.Error("a pull request with no recorded participants credited somebody")
	}
	if dirWork["pkg/scheduler"]["author@x.com"] != 20 {
		t.Error("existing commit work was disturbed")
	}
}

// TestAddReviewCreditJoinsAcrossRepositories pins the two halves of the join
// that a bare pull request number got wrong.
//
// A number is unique within a repository and not across a set of them, so
// indexing two repositories that both have a pull request 5 once credited one
// repository's reviewers into the other's directories, and which one won
// depended on the order concurrent workers finished in. The reference now
// carries the repository. A clone with no remote cannot supply one, so it
// falls back to the number, but only where that number belongs to a single
// repository.
func TestAddReviewCreditJoinsAcrossRepositories(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name       string
		PullDirs   map[connector.PullRef][]string
		PullPeople map[connector.PullRef][]string
		Dir        string
		Who        string
		WantCredit float64
	}{{ // Test 0: Two repositories share a number and stay apart.
		Name: "same number in two repositories",
		PullDirs: map[connector.PullRef][]string{
			{Repo: "acme/a", Number: 5}: {"a/svc"},
		},
		PullPeople: map[connector.PullRef][]string{
			{Repo: "acme/b", Number: 5}: {"b-reviewer"},
		},
		Dir: "a/svc", Who: "github:b-reviewer", WantCredit: 0,
	}, { // Test 1: A clone with no remote still joins on an unambiguous number.
		Name: "clone with no remote",
		PullDirs: map[connector.PullRef][]string{
			{Number: 5}: {"a/svc"},
		},
		PullPeople: map[connector.PullRef][]string{
			{Repo: "acme/a", Number: 5}: {"a-reviewer"},
		},
		Dir: "a/svc", Who: "github:a-reviewer", WantCredit: reviewWeight,
	}, { // Test 2: An anonymous clone never guesses between two claimants.
		Name: "ambiguous number from an anonymous clone",
		PullDirs: map[connector.PullRef][]string{
			{Number: 5}: {"a/svc"},
		},
		PullPeople: map[connector.PullRef][]string{
			{Repo: "acme/a", Number: 5}: {"a-reviewer"},
			{Repo: "acme/b", Number: 5}: {"b-reviewer"},
		},
		Dir: "a/svc", Who: "github:a-reviewer", WantCredit: 0,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			dirWork := map[string]map[string]float64{test.Dir: {"author@x.com": 20}}
			totals := map[string]float64{"author@x.com": 400}
			AddReviewCredit(dirWork, totals, test.PullDirs, test.PullPeople)
			if got := dirWork[test.Dir][test.Who]; got != test.WantCredit {
				t.Errorf("%s credit for %s = %v, want %v",
					test.Dir, test.Who, got, test.WantCredit)
			}
		})
	}
}

// TestPlaceLeadsSharesAndBus verifies the two numbers the risk view reads.
// Shares must be worked out against the discounted holder total rather than
// against Work, which counts commits before the breadth discount and would
// make the ratio meaningless; and the bus factor must count holders beyond the
// ones the list is cut to, or a dominant place and a crowded one look alike.
func TestPlaceLeadsSharesAndBus(t *testing.T) {
	t.Parallel()
	ix := index.New()
	recs := []connector.Record{
		{Kind: connector.KindPerson, Name: "Sole", Email: "sole@x.com",
			Topics: []string{"held"}, Source: "git"},
	}
	crowdWork := map[string]float64{}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		recs = append(recs, connector.Record{Kind: connector.KindPerson, Name: n,
			Email: n + "@x.com", Topics: []string{"crowd"}, Source: "git"})
		crowdWork[n+"@x.com"] = 20
	}
	ix.Build(recs)
	ix.Canonicalize()

	dirWork := map[string]map[string]float64{
		"held":  {"sole@x.com": 95, "a@x.com": 5},
		"crowd": crowdWork,
	}
	totals := map[string]float64{"sole@x.com": 100}
	for e, w := range crowdWork {
		totals[e] = w
	}
	totals["a@x.com"] = 25

	byDir := map[string]Place{}
	for _, p := range PlaceLeads(ix, dirWork, totals, 10, 3) {
		byDir[p.Dir] = p
	}

	held, ok := byDir["held"]
	if !ok {
		t.Fatal("held is missing")
	}
	if held.Bus != 1 {
		t.Errorf("held bus = %d, want 1: one person covers the concentration cut", held.Bus)
	}
	if held.People != 2 {
		t.Errorf("held people = %d, want 2", held.People)
	}
	if s := held.Holders[0].Share; s < 0.9 || s > 1 {
		t.Errorf("held top share = %.3f, want most of it; Work is %.0f, so dividing by "+
			"Work instead of the discounted total would not give this", s, held.Work)
	}

	crowd, ok := byDir["crowd"]
	if !ok {
		t.Fatal("crowd is missing")
	}
	if crowd.Bus <= 1 {
		t.Errorf("crowd bus = %d, want more than one: five equal holders is not a "+
			"place resting on one person", crowd.Bus)
	}
	if len(crowd.Holders) != 3 {
		t.Errorf("crowd holders = %d, want the list cut to 3", len(crowd.Holders))
	}
	if crowd.People != 5 {
		t.Errorf("crowd people = %d, want 5 counted before the cut", crowd.People)
	}
}

// TestMostFragileRanksExposureNotSize verifies the risk view's ordering. Size
// ranking puts the directories everybody has touched on top, which is exactly
// where a finding cannot be, so fewest holders must win over most work.
func TestMostFragileRanksExposureNotSize(t *testing.T) {
	t.Parallel()
	in := []Place{
		{Dir: "huge", Work: 1000, Bus: 40},
		{Dir: "fragile-small", Work: 20, Bus: 1},
		{Dir: "fragile-big", Work: 300, Bus: 1},
		{Dir: "middling", Work: 100, Bus: 5},
	}
	got := MostFragile(in, 3)
	want := []string{"fragile-big", "fragile-small", "middling"}
	if len(got) != len(want) {
		t.Fatalf("got %d places, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Dir != w {
			t.Errorf("place %d = %s, want %s (bus ascending, then busiest)", i, got[i].Dir, w)
		}
	}
	if in[0].Dir != "huge" {
		t.Error("MostFragile reordered its input; callers share the slice with the deliverable")
	}
}
