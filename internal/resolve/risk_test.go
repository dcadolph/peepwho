package resolve

import (
	"slices"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
)

// TestRisk checks concentration scoring flags a sole-expert topic as critical,
// a shared topic as elevated, and that Departure lists what leaves with a person.
func TestRisk(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Ruth", Email: "ruth@x.com", Topics: []string{"auth", "secrets"}, Source: "t"},
		{Kind: connector.KindPerson, Name: "Ann", Email: "ann@x.com", Topics: []string{"billing"}, Source: "t"},
		{Kind: connector.KindPerson, Name: "Bob", Email: "bob@x.com", Topics: []string{"billing"}, Source: "t"},
	})
	ix.Canonicalize()

	byTopic := make(map[string]TopicRisk)
	for _, r := range Risk(ix, 0) {
		byTopic[r.Topic] = r
	}
	if a := byTopic["auth"]; a.Level != "critical" || a.BusFactor != 1 {
		t.Errorf("auth = %+v, want critical bus factor 1", a)
	}
	if b := byTopic["billing"]; b.Level != "elevated" || b.BusFactor != 2 {
		t.Errorf("billing = %+v, want elevated bus factor 2", b)
	}

	imp := Departure(ix, "ruth")
	if !slices.Equal(imp.Sole, []string{"auth", "secrets"}) {
		t.Errorf("Ruth sole topics = %v, want [auth secrets]", imp.Sole)
	}
	if none := Departure(ix, "nobodyxyz"); none.Person != "" {
		t.Errorf("unmatched query resolved to %q, want empty", none.Person)
	}
}
