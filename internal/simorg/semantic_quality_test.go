package simorg

import (
	"context"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/llm"
)

// ollamaURL is where a local model serves, when one is running.
const ollamaURL = "http://localhost:11434"

// TestSemanticClosesTheBlindGap measures what a model actually adds. Keyword
// mode scores 0.00 on questions that share no vocabulary with the subject,
// by definition. Semantic mode exists to close exactly that gap, and this is
// the number that says how far it does. It needs a local Ollama with an embed
// model pulled, so it skips cleanly on machines without one.
func TestSemanticClosesTheBlindGap(t *testing.T) {
	t.Parallel()
	// Two clients, one per retrieval side, exactly as the binary builds them:
	// indexing marks documents and asking marks queries.
	docs := llm.New("", llm.WithBaseURL(ollamaURL), llm.WithEmbedModel("nomic-embed-text"),
		llm.WithEmbedTask(llm.EmbedDocuments))
	queries := llm.New("", llm.WithBaseURL(ollamaURL), llm.WithEmbedModel("nomic-embed-text"))
	ctx := context.Background()
	if _, err := queries.Embed(ctx, "reachable"); err != nil {
		t.Skipf("no local model to measure against: %v", err)
	}

	built, err := Build(Spec{Seed: 7}, t.TempDir())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := built.Embed(ctx, docs); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	for _, tc := range []struct {
		Name string
		Kind Kind
		Min  float64
	}{
		// Fusion folds the word ranking in, so the friendly cases must stay
		// as good as keyword alone. Measured: exact 1.00, anchored 0.83.
		{"exact", KindWhoKnows, 0.90},
		{"anchored", KindAnchored, 0.70},
		// Blind is the ceiling of one vector per person on this model,
		// measured at 0.25. The floor is set under it to catch regression,
		// not to flatter it: keyword alone scores 0.08 here, and closing the
		// rest of the gap needs a better representation, not a better test.
		{"blind", KindBlind, 0.15},
	} {
		t.Run(tc.Name, func(t *testing.T) {
			score := built.ScoreSemantic(ctx, queries, tc.Kind, 5)
			t.Logf("semantic %-9s %s", tc.Name+":", score)
			if score.Precision3() < tc.Min {
				t.Errorf("semantic %s p@3 = %.2f, want at least %.2f. Missed: %s",
					tc.Name, score.Precision3(), tc.Min, strings.Join(score.Missed, "; "))
			}
		})
	}
}
