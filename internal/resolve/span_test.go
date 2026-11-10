package resolve

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// TestSoleSpansFindOnePersonConnections checks whotoask reports a connection
// between two subjects that only one person has ever made. Counting experts per
// subject cannot find these: both areas are well covered, and what rests on one
// person is not either subject but the knowledge that they belong together.
func TestSoleSpansFindOnePersonConnections(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		// Both subjects have several experts each.
		{Kind: connector.KindPerson, Name: "Ada", Email: "ada@x.com",
			Topics: []string{"billing", "billing"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Bo", Email: "bo@x.com",
			Topics: []string{"billing"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Cy", Email: "cy@x.com",
			Topics: []string{"ledger", "ledger"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Di", Email: "di@x.com",
			Topics: []string{"ledger"}, Source: "git"},
		// But only one person has ever worked across the two.
		{Kind: connector.KindPerson, Name: "Bridge", Email: "bridge@x.com",
			Topics: []string{"billing", "ledger"}, Source: "git"},
		{Kind: connector.KindTopic, Name: "billing", Source: "git",
			Links: []connector.TopicLink{{To: "ledger", Weight: 0.5, Witnesses: 1, Sole: "bridge@x.com"}}},
		// And a connection several people have made, which is not a finding.
		{Kind: connector.KindTopic, Name: "ledger", Source: "git",
			Links: []connector.TopicLink{{To: "billing", Weight: 0.5, Witnesses: 1, Sole: "bridge@x.com"},
				{To: "payroll", Weight: 0.4, Witnesses: 4}}},
		{Kind: connector.KindPerson, Name: "Ez", Email: "ez@x.com",
			Topics: []string{"payroll", "payroll"}, Source: "git"},
	})
	ix.Canonicalize()

	got := SoleSpans(ix, 0)
	if len(got) != 1 {
		t.Fatalf("spans = %+v, want only the connection one person has made", got)
	}
	if diff := cmp.Diff([]string{"billing", "ledger"}, got[0].Topics); diff != "" {
		t.Errorf("subjects (-want +got):\n%s", diff)
	}
	if got[0].Person != "Bridge" {
		t.Errorf("person = %q, want the one who has worked across both", got[0].Person)
	}
	// The point of the finding is that the subjects are not short of experts.
	if got[0].Experts < 4 {
		t.Errorf("experts = %d, want the people holding either subject counted", got[0].Experts)
	}
}

// TestSoleSpansIgnoreUnestablishedSubjects checks a tie to something nobody
// holds is not reported. Being changed alongside a subject does not make a word
// into one, and a finding about a word helps nobody.
func TestSoleSpansIgnoreUnestablishedSubjects(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Ada", Email: "ada@x.com",
			Topics: []string{"billing"}, Source: "git"},
		{Kind: connector.KindTopic, Name: "billing", Source: "git",
			Links: []connector.TopicLink{{To: "scratchpad", Weight: 0.9, Witnesses: 1, Sole: "ada@x.com"}}},
	})
	ix.Canonicalize()
	if got := SoleSpans(ix, 0); len(got) != 0 {
		t.Errorf("spans = %+v, want nothing: scratchpad is not a subject anybody holds", got)
	}
}

// TestSoleSpansNeedsAStrongTieOnBothSides checks a connection is only reported
// when it is among the strongest ties for both subjects.
//
// Measured on a real issue tracker, this is what separates a finding from noise.
// "Only one person has worked across documentation and kubernetes" is true and
// worthless: the two are barely tied, and hundreds of people hold each. The tie
// being the strongest one both subjects have is what makes the crossing mean
// something.
func TestSoleSpansNeedsAStrongTieOnBothSides(t *testing.T) {
	t.Parallel()
	// A subject tied to many others, where the pairing under test sits far down
	// the list, against a pairing that is the best tie either side has.
	topics := map[model.ID]*model.Topic{
		"strongA": {ID: "strongA", Name: "strongA", Curated: true, Near: map[model.ID]model.Tie{
			"strongB": {Weight: 0.5, Witnesses: 1, Sole: "ada@x.com"},
		}},
		"strongB": {ID: "strongB", Name: "strongB", Curated: true, Near: map[model.ID]model.Tie{
			"strongA": {Weight: 0.5, Witnesses: 1, Sole: "ada@x.com"},
		}},
		"broad": {ID: "broad", Name: "broad", Curated: true, Near: map[model.ID]model.Tie{}},
		"weak":  {ID: "weak", Name: "weak", Curated: true, Near: map[model.ID]model.Tie{}},
	}
	// broad is tied to many subjects more strongly than it is to weak, so the
	// crossing between them sits well down its list.
	for i := range maxSpanRank + 3 {
		other := model.ID(fmt.Sprintf("filler%d", i))
		topics[other] = &model.Topic{ID: other, Name: string(other), Curated: true,
			Near: map[model.ID]model.Tie{"broad": {Weight: 0.4}}}
		topics["broad"].Near[other] = model.Tie{Weight: 0.4}
	}
	topics["broad"].Near["weak"] = model.Tie{Weight: 0.01, Witnesses: 1, Sole: "bo@x.com"}
	topics["weak"].Near["broad"] = model.Tie{Weight: 0.01, Witnesses: 1, Sole: "bo@x.com"}

	ix := index.New()
	ix.Graph.Topics = topics
	ix.Graph.People = map[model.ID]*model.Person{
		"ada@x.com": {ID: "ada@x.com", Name: "Ada", Topics: map[model.ID]float64{
			"strongA": 3, "strongB": 3, "broad": 2, "weak": 2,
		}},
		"bo@x.com": {ID: "bo@x.com", Name: "Bo", Topics: map[model.ID]float64{
			"strongA": 2, "strongB": 2, "broad": 3, "weak": 3,
		}},
	}
	got := SoleSpans(ix, 0)
	var pairs []string
	for _, s := range got {
		pairs = append(pairs, strings.Join(s.Topics, "+"))
	}
	want := []string{"strongA+strongB"}
	if diff := cmp.Diff(want, pairs, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("connections (-want +got):\n%s", diff)
	}
}

// TestSoleSpansJoinCrossingsThatShareAPerson checks the crossings one person
// alone makes are reported as the body of work they are, not one row at a time.
//
// On a real repository the same person was the only one crossing between four
// subjects, which arrived as five rows saying nearly the same thing and pushed
// genuinely separate findings off the end of the report. It is one thing to
// know and one person to lose.
func TestSoleSpansJoinCrossingsThatShareAPerson(t *testing.T) {
	t.Parallel()
	tie := func(w float64, who model.ID) model.Tie {
		return model.Tie{Weight: w, Witnesses: 1, Sole: who}
	}
	topics := map[model.ID]*model.Topic{
		// Three subjects one person crosses between, and a separate pair
		// crossed by somebody else.
		"alpha": {ID: "alpha", Name: "alpha", Curated: true, Near: map[model.ID]model.Tie{
			"beta": tie(0.5, "ada@x.com"), "gamma": tie(0.4, "ada@x.com")}},
		"beta": {ID: "beta", Name: "beta", Curated: true, Near: map[model.ID]model.Tie{
			"alpha": tie(0.5, "ada@x.com"), "gamma": tie(0.3, "ada@x.com")}},
		"gamma": {ID: "gamma", Name: "gamma", Curated: true, Near: map[model.ID]model.Tie{
			"alpha": tie(0.4, "ada@x.com"), "beta": tie(0.3, "ada@x.com")}},
		"delta": {ID: "delta", Name: "delta", Curated: true, Near: map[model.ID]model.Tie{
			"epsilon": tie(0.6, "bo@x.com")}},
		"epsilon": {ID: "epsilon", Name: "epsilon", Curated: true, Near: map[model.ID]model.Tie{
			"delta": tie(0.6, "bo@x.com")}},
	}
	ix := index.New()
	ix.Graph.Topics = topics
	ix.Graph.People = map[model.ID]*model.Person{
		"ada@x.com": {ID: "ada@x.com", Name: "Ada", Topics: map[model.ID]float64{
			"alpha": 3, "beta": 3, "gamma": 3}},
		"bo@x.com": {ID: "bo@x.com", Name: "Bo", Topics: map[model.ID]float64{
			"alpha": 1, "beta": 1, "gamma": 1, "delta": 3, "epsilon": 3}},
	}

	got := SoleSpans(ix, 0)
	type finding struct {
		Topics []string
		Person string
	}
	var out []finding
	for _, s := range got {
		out = append(out, finding{Topics: s.Topics, Person: s.Person})
	}
	want := []finding{
		{Topics: []string{"alpha", "beta", "gamma"}, Person: "Ada"},
		{Topics: []string{"delta", "epsilon"}, Person: "Bo"},
	}
	if diff := cmp.Diff(want, out, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("findings (-want +got):\n%s", diff)
	}
	// The weakest crossing is what the group rests on, so it is what is said.
	if got[0].Together != 0.3 {
		t.Errorf("together = %v, want the weakest crossing 0.3 holding the group", got[0].Together)
	}
}

// TestASweepingRefactorIsNotEvidenceOfConnection is the property the whole
// finding rests on, and the one that decides whether it survives being checked.
//
// Somebody who touches nine hundred areas in one commit has run a codemod, not
// learned how those areas relate. If that counted, every sole connection would
// dissolve the moment a repository had a formatting pass, and anyone verifying a
// finding against the history would find the refactor author and conclude whotoask
// was wrong. Measured on home-assistant/core: of four findings checked by hand,
// every rival author was a 900-to-1271-component refactor and the named person
// was the only one who had done focused work across the areas.
func TestASweepingRefactorIsNotEvidenceOfConnection(t *testing.T) {
	t.Parallel()

	ix := index.New()
	var recs []connector.Record
	// One person does real, narrow work across two areas, several times.
	for i := 0; i < 6; i++ {
		recs = append(recs, connector.Record{
			Kind: connector.KindPerson, Name: "Focused Dev", Email: "focused@corp.com",
			Topics: []string{"billing-retries", "payment-gateway"}, Source: "git",
		})
	}
	// Somebody else touches both, and hundreds of other things, in one sweep.
	wide := []string{"billing-retries", "payment-gateway"}
	for i := 0; i < 200; i++ {
		wide = append(wide, fmt.Sprintf("area-%d", i))
	}
	recs = append(recs, connector.Record{
		Kind: connector.KindPerson, Name: "Codemod Runner", Email: "sweep@corp.com",
		Topics: wide, Source: "git",
	})
	ix.Build(recs)
	ix.AutoJoin()
	ix.Canonicalize()

	// The refactor must not make the connection look shared. Whether a span is
	// reported at all depends on tie strength, but if one IS reported for these
	// two areas it has to name the person who did the focused work.
	for _, s := range SoleSpans(ix, 0) {
		joins := 0
		for _, topic := range s.Topics {
			if topic == "billing-retries" || topic == "payment-gateway" {
				joins++
			}
		}
		if joins < 2 {
			continue
		}
		if s.Person == "Codemod Runner" {
			t.Errorf("a sweep over 202 areas was read as knowing how two of them connect: %+v", s)
		}
	}
}
