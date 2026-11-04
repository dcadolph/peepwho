package eval

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
)

// owns declares an area for someone on paper, the way a CODEOWNERS file does.
func owns(name, email string, topics ...string) connector.Record {
	return connector.Record{
		Kind: connector.KindPerson, Name: name, Email: email,
		Topics: topics, Source: "codeowners",
	}
}

// works records someone actually doing the work, repeated to carry weight the
// way a run of commits does.
func works(name, email, topic string, times int) connector.Record {
	t := make([]string, times)
	for i := range t {
		t[i] = topic
	}
	return connector.Record{
		Kind: connector.KindPerson, Name: name, Email: email,
		Topics: t, Source: "git",
	}
}

// TestMeasureSplitsDisagreementByCause is the test that matters. Agreement on
// its own is not actionable: the same score can mean peepwho ranks badly, that
// the declared owner has left, or that their work was never linked to their
// handle. Each is a different fix, so each has to land in its own bucket.
func TestMeasureSplitsDisagreementByCause(t *testing.T) {
	t.Parallel()

	ix := index.New()
	ix.Build([]connector.Record{
		// Held: the declared owner is also the one doing the work.
		owns("Dana Held", "dana@corp.com", "billing-retries"),
		works("Dana Held", "dana@corp.com", "billing-retries", 40),

		// Trailing: the owner works here, but somebody else does far more.
		owns("Eve Trail", "eve@corp.com", "search-indexing"),
		works("Eve Trail", "eve@corp.com", "search-indexing", 4),
		works("Frank Lead", "frank@corp.com", "search-indexing", 50),

		// Unworked: the owner is busy elsewhere and never touches what they own.
		owns("Gus Paper", "gus@corp.com", "sso-login"),
		works("Gus Paper", "gus@corp.com", "release-tooling", 30),
		works("Hank Real", "hank@corp.com", "sso-login", 40),

		// Silent: the owner has no recorded work anywhere. On real data this is
		// nearly always an identity that never got joined to its commits.
		owns("ghost-handle", "", "payroll-taxes"),
		works("Ivy Stand", "ivy@corp.com", "payroll-taxes", 25),
	})
	ix.AutoJoin()
	ix.Canonicalize()

	got := Measure(ix)

	if got.Declared != 4 {
		t.Fatalf("declared = %d, want 4 areas with an owner of record; got %+v", got.Declared, got)
	}
	for _, c := range []struct {
		Name string
		Got  int
		Want int
		Why  string
	}{
		{"held", got.Held, 1, "the owner still leads their own area"},
		{"trailing", got.Trailing, 1, "an owner out-worked in their own area"},
		{"unworked", got.Unworked, 1, "an owner who works elsewhere but never here"},
		{"silent", got.Silent, 1, "an owner with no recorded work at all"},
	} {
		if c.Got != c.Want {
			t.Errorf("%s = %d, want %d: %s", c.Name, c.Got, c.Want, c.Why)
		}
	}
	if want := 0.25; got.Agreement != want {
		t.Errorf("agreement = %v, want %v", got.Agreement, want)
	}
	// Only the held area and the trailing one are questions about ranking. The
	// owner who never worked here and the one with no work at all could not be
	// ranked first by any ranking, so scoring them measures what was indexed.
	if got.Answerable != 2 {
		t.Errorf("answerable = %d, want 2: the held area and the trailing one", got.Answerable)
	}
	if want := 0.5; got.Ranked != want {
		t.Errorf("ranked = %v, want %v: right in one of the two it could answer", got.Ranked, want)
	}
	// The gap between the two is the whole reason both are reported. Reading
	// agreement as a ranking score understated a real project by 45 points.
	if got.Ranked <= got.Agreement {
		t.Errorf("ranked %v is not above agreement %v; the split has stopped working", got.Ranked, got.Agreement)
	}
	// The buckets have to account for every declared area, or a disagreement has
	// no cause recorded and the score cannot be interpreted at all.
	if sum := got.Held + got.Silent + got.Unworked + got.Trailing; sum != got.Declared {
		t.Errorf("buckets sum to %d but %d areas were declared; a cause is being dropped", sum, got.Declared)
	}
	// Both sources have to be named, since a comparison across different sources
	// is meaningless and Compare relies on this to refuse one.
	if diff := cmp.Diff([]string{"codeowners", "git"}, got.Sources, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("sources mismatch (-want +got):\n%s", diff)
	}
}

// TestMeasureIsStable checks the same index measures the same twice. Map order
// varies between runs, and a score that moves on its own cannot be used to judge
// whether a change helped.
func TestMeasureIsStable(t *testing.T) {
	t.Parallel()

	var recs []connector.Record
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("Person %d", i)
		email := fmt.Sprintf("p%d@corp.com", i)
		recs = append(recs,
			owns(name, email, fmt.Sprintf("area-%d", i%5)),
			works(name, email, fmt.Sprintf("area-%d", i%5), i+1),
			works(name, email, fmt.Sprintf("shared-topic-%d", i%3), 20-i),
		)
	}
	ix := index.New()
	ix.Build(recs)
	ix.AutoJoin()
	ix.Canonicalize()

	first := Measure(ix)
	for i := 0; i < 6; i++ {
		if diff := cmp.Diff(first, Measure(ix), cmpopts.EquateEmpty()); diff != "" {
			t.Fatalf("measurement %d differs from the first (-first +again):\n%s", i+2, diff)
		}
	}
}

// TestCompareRefusesDifferentSources guards the mistake this harness exists to
// prevent: reading movement as quality when the two runs were not measuring the
// same thing. Adding a source moves nearly every number here.
func TestCompareRefusesDifferentSources(t *testing.T) {
	t.Parallel()
	before := Result{Agreement: 0.5, Sources: []string{"git"}}
	after := Result{Agreement: 0.9, Sources: []string{"git", "slack"}}
	if _, ok := Compare(before, after); ok {
		t.Error("compared two runs over different sources; the movement means nothing")
	}
	after.Sources = []string{"git"}
	if _, ok := Compare(before, after); !ok {
		t.Error("refused to compare two runs over the same sources")
	}
}

// TestCompareLeadsWithRegressions checks a change that cost accuracy cannot hide
// under a list of things that merely got bigger.
func TestCompareLeadsWithRegressions(t *testing.T) {
	t.Parallel()
	before := Result{Ranked: 0.90, Agreement: 0.70, Held: 70, Declared: 100, Answerable: 78, Unlinked: 10, Topics: 500, Sources: []string{"git"}}
	after := Result{Ranked: 0.82, Agreement: 0.64, Held: 64, Declared: 100, Answerable: 78, Unlinked: 4, Topics: 900, Sources: []string{"git"}}

	changes, ok := Compare(before, after)
	if !ok {
		t.Fatal("refused to compare comparable runs")
	}
	if len(changes) == 0 {
		t.Fatal("nothing reported as moved")
	}
	if changes[0].Name != "ranked" {
		t.Errorf("first change is %q, want ranked: the number a ranking change moves has to lead", changes[0].Name)
	}
	if changes[0].Better {
		t.Error("a fall in agreement was reported as an improvement")
	}
	byName := map[string]Change{}
	for _, c := range changes {
		byName[c.Name] = c
	}
	// Falling unlinked is an improvement; more subjects is neither good nor bad.
	if u := byName["unlinked"]; !u.Scored || !u.Better {
		t.Errorf("unlinked 10 -> 4 scored=%v better=%v, want a scored improvement", u.Scored, u.Better)
	}
	if tp := byName["topics"]; tp.Scored {
		t.Error("topics was scored, but finding more subjects is context, not quality")
	}
	// Nothing that stayed put may appear, or every report is a wall of noise.
	if _, ok := byName["declared"]; ok {
		t.Error("declared did not move but was reported")
	}
}

// area is one recorded verdict.
func area(topic string, held, answerable bool) Area {
	return Area{Topic: topic, Held: held, Answerable: answerable}
}

// TestCompareAreasIgnoresQuestionsOnlyOneRunCouldAnswer is the whole reason this
// comparison exists. Widening a history window on a real project moved the
// overall score from 72.1% to 57.1% while also raising the areas scored from 405
// to 629, and reading that fall as worse ranking would have been wrong: the
// extra areas were ones the narrower index never had to answer.
func TestCompareAreasIgnoresQuestionsOnlyOneRunCouldAnswer(t *testing.T) {
	t.Parallel()
	before := Result{Areas: []Area{
		area("billing-retries", true, true),
		area("search-indexing", false, true),
		// Not answerable before: its owner had no work in the window.
		area("sso-login", false, false),
	}}
	after := Result{Areas: []Area{
		area("billing-retries", true, true),
		area("search-indexing", true, true), // won
		// Now answerable, but the other run never faced it, so it cannot count.
		area("sso-login", false, true),
		// Only the later run has this area at all.
		area("payroll-taxes", false, true),
	}}

	h, ok := CompareAreas(before, after)
	if !ok {
		t.Fatal("refused a comparison that has shared areas")
	}
	if h.Areas != 2 {
		t.Errorf("compared %d areas, want 2: only those both runs could answer", h.Areas)
	}
	if h.BeforeHeld != 1 || h.AfterHeld != 2 {
		t.Errorf("held before=%d after=%d, want 1 and 2", h.BeforeHeld, h.AfterHeld)
	}
	if want := 1.0; h.Rate() != want {
		t.Errorf("rate = %v, want %v", h.Rate(), want)
	}
	if diff := cmp.Diff([]string{"search-indexing"}, h.Won, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("won mismatch (-want +got):\n%s", diff)
	}
	if len(h.Lost) != 0 {
		t.Errorf("lost = %v, want none", h.Lost)
	}
}

// TestCompareAreasNamesWhatWasLost checks a change that trades one set of areas
// for another is not reported as an improvement. The totals alone would hide it.
func TestCompareAreasNamesWhatWasLost(t *testing.T) {
	t.Parallel()
	before := Result{Areas: []Area{
		area("a-one", true, true), area("b-two", true, true), area("c-three", false, true),
	}}
	after := Result{Areas: []Area{
		area("a-one", false, true), area("b-two", true, true), area("c-three", true, true),
	}}

	h, ok := CompareAreas(before, after)
	if !ok {
		t.Fatal("refused a comparable pair")
	}
	// Two right before, two right after: the score has not moved at all, and
	// the only honest report is which areas swapped.
	if h.BeforeHeld != 2 || h.AfterHeld != 2 {
		t.Errorf("held before=%d after=%d, want 2 and 2", h.BeforeHeld, h.AfterHeld)
	}
	if diff := cmp.Diff([]string{"c-three"}, h.Won, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("won mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"a-one"}, h.Lost, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("lost mismatch (-want +got):\n%s", diff)
	}
}

// TestCompareAreasRefusesABaselineWithoutVerdicts covers a baseline saved before
// per-area verdicts existed, which must be reported as not comparable rather
// than silently scored as zero shared areas.
func TestCompareAreasRefusesABaselineWithoutVerdicts(t *testing.T) {
	t.Parallel()
	if _, ok := CompareAreas(Result{}, Result{Areas: []Area{area("a-one", true, true)}}); ok {
		t.Error("compared against a baseline that recorded no areas")
	}
	if _, ok := CompareAreas(Result{Areas: []Area{area("a-one", true, true)}}, Result{}); ok {
		t.Error("compared a run that recorded no areas")
	}
	// Two runs that share no answerable area have nothing to say either.
	no := Result{Areas: []Area{area("a-one", true, false)}}
	if _, ok := CompareAreas(no, Result{Areas: []Area{area("b-two", true, true)}}); ok {
		t.Error("reported a comparison over no shared areas")
	}
}
