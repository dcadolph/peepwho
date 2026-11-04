package index

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/peepwho/internal/model"
)

// TestLengthsAreOrderIndependent pins the BM25 length table to one set of
// bytes for one posting set. Float addition is not associative, so summing in
// map order put different last bits in the table on different loads of one
// index, and those bits reach every score through the verbosity normalizer.
func TestLengthsAreOrderIndependent(t *testing.T) {
	t.Parallel()
	postings := map[string]map[model.ID]float64{
		"t1": {"kim@x.com": 0.1, "lee@x.com": 0.7},
		"t2": {"kim@x.com": 0.2, "lee@x.com": 0.011},
		"t3": {"kim@x.com": 0.3},
		"t4": {"kim@x.com": 0.4, "lee@x.com": 0.013},
		"t5": {"kim@x.com": 0.5},
	}
	first := lengthsOf(postings)
	for range 30 {
		again := lengthsOf(postings)
		if diff := cmp.Diff(first.byID, again.byID); diff != "" {
			t.Fatalf("length table changed between runs on one posting set (-first +now):\n%s", diff)
		}
		if first.avg != again.avg {
			t.Fatalf("average length changed between runs: %v vs %v", first.avg, again.avg)
		}
	}
}
