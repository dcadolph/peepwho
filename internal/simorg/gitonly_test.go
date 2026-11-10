package simorg

import (
	"context"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// TestGitOnlyIndexReportsRisk covers the shape of a first trial: somebody points
// whotoask at a repository and nothing else, because that needs no credentials and
// no chat integration. Knowledge risk is the thing they were sold, so an index
// built from commit history alone has to produce it. It once did not, silently,
// because commit history marked none of its subjects as stated and every view
// that separates a real subject from a passing word then saw an empty graph.
func TestGitOnlyIndexReportsRisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := BuildGitRepo(dir); err != nil {
		t.Fatalf("build repo: %v", err)
	}
	recs, err := connector.NewGitHistory(connector.GitOptions{
		Paths: []string{dir}, SinceDays: 3650, MaxCommits: 5000,
	}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("the generated repository produced no records")
	}
	ix := index.New()
	ix.Build(recs)
	ix.AutoJoin()
	ix.Canonicalize()

	var stated int
	for _, topic := range ix.Graph.Topics {
		if topic.Salient() {
			stated++
		}
	}
	if stated == 0 {
		t.Fatalf("none of the %d subjects are stated, so every view that filters on that is empty",
			len(ix.Graph.Topics))
	}
	if risks := resolve.Risk(ix, 10); len(risks) == 0 {
		t.Errorf("risk reported nothing from %d people and %d subjects",
			len(ix.Graph.People), len(ix.Graph.Topics))
	}
	if dir := resolve.BuildDirectory(ix); len(dir.Topics) == 0 {
		t.Error("the directory lists no subjects")
	}
}
