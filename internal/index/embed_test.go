package index

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
)

// fakeEmbedder maps text to a small vector over a fixed vocabulary, so cosine
// similarity is deterministic in tests.
type fakeEmbedder struct{}

// Embed sets a dimension for each vocabulary word present in text.
func (fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	vocab := []string{"retries", "kafka", "billing", "infra"}
	t := strings.ToLower(text)
	vec := make([]float32, len(vocab))
	for i, w := range vocab {
		if strings.Contains(t, w) {
			vec[i] = 1
		}
	}
	return vec, nil
}

// TestSemanticSearch verifies embedding and cosine ranking pick the right person.
func TestSemanticSearch(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Name: "Jane Roe", Email: "jane@x.com", Team: "Billing", Topics: []string{"retries"}},
		{Name: "Bob Lee", Email: "bob@x.com", Team: "Infra", Topics: []string{"kafka"}},
	})
	if ix.HasEmbeddings() {
		t.Fatal("index should have no embeddings before Embed")
	}
	if err := ix.Embed(context.Background(), fakeEmbedder{}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if !ix.HasEmbeddings() {
		t.Fatal("index should have embeddings after Embed")
	}

	q, _ := fakeEmbedder{}.Embed(context.Background(), "retries")
	got := ix.SemanticPeople(q, 5)
	if len(got) == 0 || got[0].Person.Email != "jane@x.com" {
		t.Fatalf("top semantic person = %v, want jane@x.com", got)
	}
	if got[0].Strength <= 0 || got[0].Strength != got[0].Score {
		t.Errorf("strength = %v score = %v, want similarity as strength", got[0].Strength, got[0].Score)
	}
}

// TestEmbedSaveLoad verifies vectors survive a round trip to disk.
func TestEmbedSaveLoad(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{{Name: "Jane", Email: "jane@x.com", Topics: []string{"retries"}}})
	if err := ix.Embed(context.Background(), fakeEmbedder{}); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	path := filepath.Join(t.TempDir(), "index.json")
	if err := ix.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.HasEmbeddings() {
		t.Fatal("embeddings lost on save and load")
	}
	q, _ := fakeEmbedder{}.Embed(context.Background(), "retries")
	if got := loaded.SemanticPeople(q, 1); len(got) != 1 || got[0].Person.Email != "jane@x.com" {
		t.Fatalf("after load, semantic top = %v, want jane@x.com", got)
	}
}

// TestSemanticStrengthIsLimitIndependent pins the field statistics to the
// whole field rather than the truncated ranking. Measured after the cut, the
// same match reported a different strength at limit 2 than at limit 50, and a
// channel's strength was the raw cosine, whose ~0.6 baseline between unrelated
// texts states a certainty nothing earned.
func TestSemanticStrengthIsLimitIndependent(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Name: "A", Email: "a@x.com", Topics: []string{"retries", "kafka", "billing"}},
		{Name: "B", Email: "b@x.com", Topics: []string{"retries"}},
		{Name: "C", Email: "c@x.com", Topics: []string{"kafka"}},
		{Name: "D", Email: "d@x.com", Topics: []string{"retries", "billing", "infra"}},
		{Name: "E", Email: "e@x.com", Topics: []string{"kafka", "billing"}},
		{Kind: connector.KindChannel, Name: "one", Topics: []string{"retries", "kafka", "billing"}},
		{Kind: connector.KindChannel, Name: "two", Topics: []string{"retries"}},
		{Kind: connector.KindChannel, Name: "three", Topics: []string{"kafka"}},
		{Kind: connector.KindChannel, Name: "four", Topics: []string{"retries", "billing", "infra"}},
		{Kind: connector.KindChannel, Name: "five", Topics: []string{"kafka", "billing"}},
	})
	if err := ix.Embed(context.Background(), fakeEmbedder{}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	q, _ := fakeEmbedder{}.Embed(context.Background(), "retries kafka")

	small, full := ix.SemanticPeople(q, 2), ix.SemanticPeople(q, 0)
	if len(small) == 0 || len(full) == 0 {
		t.Fatal("the query matched nobody")
	}
	if small[0].Person.ID != full[0].Person.ID {
		t.Fatalf("top person differs by limit: %s vs %s", small[0].Person.ID, full[0].Person.ID)
	}
	if small[0].Strength != full[0].Strength {
		t.Errorf("person strength depends on the limit: %v at 2, %v unlimited",
			small[0].Strength, full[0].Strength)
	}

	chSmall, chFull := ix.SemanticChannels(q, 2), ix.SemanticChannels(q, 0)
	if len(chSmall) == 0 || len(chFull) == 0 {
		t.Fatal("the query matched no channels")
	}
	if chSmall[0].Strength != chFull[0].Strength {
		t.Errorf("channel strength depends on the limit: %v at 2, %v unlimited",
			chSmall[0].Strength, chFull[0].Strength)
	}
	if chSmall[0].Strength >= chSmall[0].Score {
		t.Errorf("channel strength %v is not below the raw cosine %v; the standout correction is missing",
			chSmall[0].Strength, chSmall[0].Score)
	}
}

// TestSemanticTopicsFlatFieldSaysNothing checks the flat-field verdict is
// reached on the whole field at every limit. Judged on a truncated top two,
// a query that suits every subject equally used to come back with confident
// names attached, exactly the answer the detector exists to withhold.
func TestSemanticTopicsFlatFieldSaysNothing(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{{Name: "Kim", Email: "kim@x.com",
		Topics: []string{"retries-a", "retries-b", "retries-c", "retries-d", "retries-e"}}})
	if err := ix.Embed(context.Background(), fakeEmbedder{}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	q, _ := fakeEmbedder{}.Embed(context.Background(), "retries")
	if got := ix.SemanticTopics(q, 0); got != nil {
		t.Fatalf("a flat field distinguished %v at no limit; the fixture is broken", got)
	}
	if got := ix.SemanticTopics(q, 2); got != nil {
		t.Errorf("a flat field dressed up as an answer at limit 2: %v", got)
	}
}

// TestStandoutStrength checks that a similarity is reported as how far a match
// stands above its field, not as the raw number. A question that suits everyone
// equally must not come back with a confident name attached.
func TestStandoutStrength(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name      string
		Ranked    []scoredID
		WantFirst float64
		WantAny   bool
	}{{ // Test 0: A flat field distinguishes nobody, so nobody is named.
		Name: "flat field",
		Ranked: []scoredID{
			{id: "a", score: 0.638}, {id: "b", score: 0.637},
			{id: "c", score: 0.636}, {id: "d", score: 0.635},
			{id: "e", score: 0.634},
		},
		WantAny: false,
	}, { // Test 1: A clear standout keeps a high strength.
		Name: "one clear match",
		Ranked: []scoredID{
			{id: "a", score: 0.90}, {id: "b", score: 0.62},
			{id: "c", score: 0.61}, {id: "d", score: 0.60},
			{id: "e", score: 0.59},
		},
		WantFirst: 1, WantAny: true,
	}, { // Test 2: Too few results to form a field, so the score stands as it is.
		Name:      "no field to judge",
		Ranked:    []scoredID{{id: "a", score: 0.7}, {id: "b", score: 0.2}},
		WantFirst: 0.7, WantAny: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			if got := discriminating(test.Ranked); got != test.WantAny {
				t.Errorf("discriminating = %v, want %v", got, test.WantAny)
			}
			if !test.WantAny {
				return
			}
			median, ok := fieldMedian(test.Ranked)
			if got := standout(test.Ranked[0].score, median, ok); got != test.WantFirst {
				t.Errorf("strength = %v, want %v", got, test.WantFirst)
			}
		})
	}
}
