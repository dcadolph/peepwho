package resolve

import (
	"context"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
)

// fakeEmbedder maps text to a small vector over a fixed vocabulary.
type fakeEmbedder struct{}

// Embed sets a dimension for each vocabulary word present in text.
func (fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	vocab := []string{"retries", "kafka"}
	t := strings.ToLower(text)
	vec := make([]float32, len(vocab))
	for i, w := range vocab {
		if strings.Contains(t, w) {
			vec[i] = 1
		}
	}
	return vec, nil
}

// embeddedIndex builds an index with embeddings over two people.
func embeddedIndex(t *testing.T) *index.Index {
	t.Helper()
	ix := index.New()
	ix.Build([]connector.Record{
		{Name: "Jane Roe", Email: "jane@x.com", Topics: []string{"retries"}},
		{Name: "Bob Lee", Email: "bob@x.com", Topics: []string{"kafka"}},
	})
	if err := ix.Embed(context.Background(), fakeEmbedder{}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	return ix
}

// TestSemanticResolve verifies the semantic resolver ranks by similarity.
func TestSemanticResolve(t *testing.T) {
	t.Parallel()
	ans, err := NewSemantic(embeddedIndex(t), fakeEmbedder{}).Resolve(context.Background(), "retries", 5)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(ans.People) == 0 || ans.People[0].Person.Email != "jane@x.com" {
		t.Fatalf("top person = %v, want jane@x.com", ans.People)
	}
}

// TestLLMUsesEmbedder verifies the LLM resolver retrieves semantically when an
// embedder and embeddings are present.
func TestLLMUsesEmbedder(t *testing.T) {
	t.Parallel()
	chat := &fakeChat{reply: `{"summary":"x","people":["jane@x.com"],"channels":[]}`}
	ans, err := NewLLM(embeddedIndex(t), chat, fakeEmbedder{}).Resolve(context.Background(), "retries", 5)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(ans.People) == 0 || ans.People[0].Person.Email != "jane@x.com" {
		t.Fatalf("top person = %v, want jane@x.com", ans.People)
	}
	if !strings.Contains(chat.gotUser, "jane@x.com") {
		t.Errorf("prompt missing semantically retrieved candidate:\n%s", chat.gotUser)
	}
}

// TestNewSemanticNil verifies the constructor guards nil dependencies.
func TestNewSemanticNil(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("expected panic")
		}
	}()
	NewSemantic(nil, fakeEmbedder{})
}
