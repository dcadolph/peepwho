package simorg

import (
	"context"
	"testing"

	"github.com/dcadolph/whotoask/internal/resolve"
)

// BenchmarkKeywordResolve times one keyword answer over the big sample company
// at the app's limit, for a question with tie reach and one without.
func BenchmarkKeywordResolve(b *testing.B) {
	ix, err := BuildBigIndex(b.TempDir())
	if err != nil {
		b.Fatalf("BuildBigIndex: %v", err)
	}
	res := resolve.NewKeyword(ix)
	for _, query := range []string{"kafka lag", "contracts"} {
		b.Run(query, func(b *testing.B) {
			for b.Loop() {
				if _, err := res.Resolve(context.Background(), query, 5); err != nil {
					b.Fatalf("Resolve(%q): %v", query, err)
				}
			}
		})
	}
}
