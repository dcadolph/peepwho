package index

import (
	"fmt"
	"sync"
	"testing"

	"github.com/dcadolph/peepwho/internal/feedback"
	"github.com/dcadolph/peepwho/internal/model"
)

// TestServeConcurrent drives Profile, SetFeedback, and Search against one index
// from many goroutines at once, the way GET /api/person and POST /api/feedback
// overlap on the serving http.Server. Every call resolves identity aliases, and
// Canonical path-compresses by writing shared maps, so an unguarded resolver
// tripped the race detector here. Run under -race.
func TestServeConcurrent(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build(sampleRecords())

	// Give each person a two-hop alias chain so Canonical must compress on the
	// serving path, not just at build time.
	people := map[string]model.ID{
		"jane":  "jane@x.com",
		"bob":   "bob@x.com",
		"carol": "carol@x.com",
		"dana":  "dana@x.com",
	}
	for name, canonical := range people {
		ix.Alias(model.ID("github:"+name), model.ID("codeowners:"+name))
		ix.Alias(model.ID("codeowners:"+name), canonical)
	}

	const workers = 12
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for w := range workers {
		done.Add(1)
		go func(seed int) {
			defer done.Done()
			start.Wait()
			for i := 0; i < 200; i++ {
				alias := fmt.Sprintf("github:%s", []string{"jane", "bob", "carol", "dana"}[(i+seed)%4])
				switch (i + seed) % 3 {
				case 0:
					ix.Profile(model.ID(alias))
				case 1:
					ix.SetFeedback([]feedback.Entry{{
						Query: "billing retries", Person: alias, Vote: feedback.Helpful,
					}})
				default:
					ix.Search("billing retries", 5)
				}
			}
		}(w)
	}
	start.Done()
	done.Wait()

	// The aliased identifiers still resolve to their canonical person.
	if _, ok := ix.Profile("github:jane"); !ok {
		t.Fatalf("Profile(github:jane) not found after concurrent churn")
	}
}
