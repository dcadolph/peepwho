package index

import (
	"fmt"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
)

// strengthIndex builds a small graph with distinct evidence classes: an
// explicit topic owner, a title match, and a person who only mentions the
// topic in free text.
func strengthIndex() *Index {
	ix := New()
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Email: "owner@corp.com", Name: "Owner",
		Topics: []string{"kafka", "streaming"}, Source: "org-csv",
	}, {
		Kind: connector.KindPerson, Email: "titled@corp.com", Name: "Titled",
		Title: "Kafka Platform Engineer", Source: "org-csv",
	}, {
		Kind: connector.KindPerson, Email: "mention@corp.com", Name: "Mention",
		Text: "saw a kafka error in the logs once", Source: "slack",
	}, {
		Kind: connector.KindChannel, Name: "kafka", Title: "kafka questions",
		Source: "slack",
	}})
	return ix
}

func TestStrength(t *testing.T) {
	t.Parallel()
	ix := strengthIndex()
	tests := []struct {
		Query   string
		Person  string
		WantMin float64
		WantMax float64
	}{{ // Test 0: Full coverage on an explicit topic is certain.
		Query: "kafka streaming", Person: "owner@corp.com", WantMin: 1, WantMax: 1,
	}, { // Test 1: Half coverage on an explicit topic halves strength.
		Query: "kafka replication", Person: "owner@corp.com", WantMin: 0.5, WantMax: 0.5,
	}, { // Test 2: A title hit is slightly weaker than a topic hit.
		Query: "kafka", Person: "titled@corp.com", WantMin: 0.85, WantMax: 0.85,
	}, { // Test 3: A free-text mention is weak evidence.
		Query: "kafka", Person: "mention@corp.com", WantMin: 0.5, WantMax: 0.5,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			for _, m := range ix.Search(test.Query, 10) {
				if string(m.Person.ID) != test.Person {
					continue
				}
				if m.Strength < test.WantMin || m.Strength > test.WantMax {
					t.Errorf("strength = %.2f, want in [%.2f, %.2f]",
						m.Strength, test.WantMin, test.WantMax)
				}
				return
			}
			t.Fatalf("person %s not in results for %q", test.Person, test.Query)
		})
	}
}

func TestChannelStrength(t *testing.T) {
	t.Parallel()
	ix := strengthIndex()
	got := ix.SearchChannels("kafka", 3)
	if len(got) == 0 {
		t.Fatal("no channel matches")
	}
	if got[0].Channel.Name != "kafka" || got[0].Strength != 1 {
		t.Errorf("top channel = %s strength %.2f, want kafka at 1.00",
			got[0].Channel.Name, got[0].Strength)
	}
}
