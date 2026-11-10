package simorg

import (
	"testing"

	"github.com/dcadolph/whotoask/internal/resolve"
)

// TestGeneratedCompanyHasWorkThatCrossesAreas checks the sample company contains
// work spanning two subjects at once, and that whotoask finds the connections.
//
// It is here because the generator once produced a company of sealed cells:
// every commit touched one area, every ticket named one label, and nothing was
// ever worked on alongside anything else. Everything passed. The connection
// graph was simply empty, and the only way that showed was by looking at it.
func TestGeneratedCompanyHasWorkThatCrossesAreas(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ix, err := BuildBigIndex(dir)
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}

	tied := 0
	for _, topic := range ix.Graph.Topics {
		if len(topic.Near) > 0 {
			tied++
		}
	}
	if tied == 0 {
		t.Fatal("no subject is worked on alongside any other, so the connection graph is empty")
	}

	spans := resolve.SoleSpans(ix, 0)
	if len(spans) == 0 {
		t.Fatal("no connection rests on a single person, which the generated company plants on purpose")
	}
	for _, s := range spans {
		if s.Person == "" {
			t.Errorf("%v rests on one person but nobody is named", s.Topics)
		}
		if s.Size() < 2 {
			t.Errorf("%v joins fewer than two subjects, which is not a crossing", s.Topics)
		}
		if s.Experts < 2 {
			t.Errorf("%v: %d experts; the finding is only interesting when the areas have some",
				s.Topics, s.Experts)
		}
	}

	// A subject belongs to exactly one finding. Seeing it twice means the
	// crossings that join up were reported separately after all, which is the
	// redundancy the grouping exists to prevent.
	where := make(map[string]int, len(spans))
	for i, s := range spans {
		for _, topic := range s.Topics {
			if prev, ok := where[topic]; ok {
				t.Errorf("%q appears in two findings, %v and %v, so one body of "+
					"joined work is being reported as several",
					topic, spans[prev].Topics, s.Topics)
			}
			where[topic] = i
		}
	}
}

// TestGeneratedCompanyHasWorkOneLeadHolds checks the sample company contains a
// joined body of work resting on a single person.
//
// It is the heaviest finding whotoask makes and the generated company had nothing
// to show for it, so the public demo displayed an empty panel where the best
// thing in the report belongs. Neighbouring subjects now deliberately fall to
// one owner, which is how a real company looks: somebody ends up holding a whole
// corner of it.
func TestGeneratedCompanyHasWorkOneLeadHolds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ix, err := BuildBigIndex(dir)
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	regions := resolve.Regions(ix, 0)
	if len(regions) == 0 {
		t.Fatal("no joined work found, so the demo shows an empty panel where the finding belongs")
	}
	for _, r := range regions {
		if r.Lead == "" {
			t.Errorf("a region of %v rests on nobody named", r.Topics)
		}
		if r.Size() < 3 {
			t.Errorf("region %v joins %d subjects; fewer than three is a pair, which risk already shows",
				r.Topics, r.Size())
		}
	}
}
