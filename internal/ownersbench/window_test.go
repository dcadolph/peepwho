package ownersbench

import (
	"fmt"
	"testing"
)

// TestSinceArgsAllHistory pins the window flag. A caller asking for a century
// of history means everything, and saying so with a date is what broke: git
// reads a 1926 cutoff through approxidate, which answers the same repository
// with every commit on one day and none on the next, because the date this
// package computes moves forward daily. The bench went green on 4 September
// and red on the 8th with no commit in between.
func TestSinceArgsAllHistory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Days int
		Want []string
	}{{ // Test 0: the bench's own window means all of history, so no flag.
		Days: 36500, Want: nil,
	}, { // Test 1: anything reaching past git's own epoch means the same.
		Days: 20000, Want: nil,
	}, { // Test 2: zero and below are not a window either.
		Days: 0, Want: nil,
	}, { // Test 3: a real window still filters.
		Days: 30, Want: []string{"--since=" + sinceDate(30)},
	}, { // Test 4: and so does the index's usual two years.
		Days: 730, Want: []string{"--since=" + sinceDate(730)},
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := sinceArgs(test.Days)
			if len(got) != len(test.Want) {
				t.Fatalf("sinceArgs(%d) = %v, want %v", test.Days, got, test.Want)
			}
			for i := range got {
				if got[i] != test.Want[i] {
					t.Errorf("sinceArgs(%d)[%d] = %q, want %q",
						test.Days, i, got[i], test.Want[i])
				}
			}
		})
	}
}

// TestDirActivitySeesAllHistory runs the activity read the bench depends on
// against the fixture and requires it to find the work. This is the assertion
// that was missing: the bench failed with "no judgeable directories" because
// this returned nothing, and nothing here looks the same as a quiet
// repository, so the cause was invisible from the failure message.
func TestDirActivitySeesAllHistory(t *testing.T) {
	// Not parallel: the fixture builds a real git repository, and the other
	// tests in this package avoid running several of those at once.
	dir := newBenchRepo(t)
	act, err := dirActivity(dir, 36500)
	if err != nil {
		t.Fatalf("dirActivity: %v", err)
	}
	if len(act) == 0 {
		t.Fatal("dirActivity found no directories in a repository that has them")
	}
	// billing is the fixture's busy directory, well above any activity floor.
	if got := act["billing"].total; got < 3 {
		t.Errorf("billing activity = %d, want at least the bench's floor of 3", got)
	}
}
