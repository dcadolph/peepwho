package index

import (
	"testing"
	"time"

	"github.com/dcadolph/peepwho/internal/connector"
)

// evalNow is the pinned clock for decay tests.
var evalNow = time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // Not modified, simplifies testing.

// recencyIndex builds an index with a pinned clock over one old and one recent
// terraform owner with identical weight, plus an old and a recent channel.
func recencyIndex(halfLife time.Duration) *Index {
	ix := New()
	ix.now = func() time.Time { return evalNow }
	ix.SetHalfLife(halfLife)
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Email: "old@corp.com", Name: "Old Owner",
		Topics: []string{"terraform"}, Source: "github", Time: evalNow.AddDate(-2, 0, 0),
	}, {
		Kind: connector.KindPerson, Email: "new@corp.com", Name: "New Owner",
		Topics: []string{"terraform"}, Source: "github", Time: evalNow.AddDate(0, 0, -7),
	}, {
		Kind: connector.KindChannel, Name: "terraform-legacy", Title: "terraform questions",
		Source: "slack", Time: evalNow.AddDate(-2, 0, 0),
	}, {
		Kind: connector.KindChannel, Name: "terraform-now", Title: "terraform questions",
		Source: "slack", Time: evalNow.AddDate(0, 0, -7),
	}})
	return ix
}

func TestDecayPrefersRecentActivity(t *testing.T) {
	t.Parallel()
	ix := recencyIndex(DefaultHalfLife)

	people := ix.Search("terraform", 2)
	if len(people) != 2 {
		t.Fatalf("people = %d, want 2", len(people))
	}
	if people[0].Person.ID != "new@corp.com" {
		t.Errorf("top person = %s, want the recent owner", people[0].Person.ID)
	}
	if people[0].Score <= people[1].Score*4 {
		t.Errorf("recent score %.2f not clearly above decayed %.2f", people[0].Score, people[1].Score)
	}

	channels := ix.SearchChannels("terraform", 2)
	if len(channels) != 2 {
		t.Fatalf("channels = %d, want 2", len(channels))
	}
	if channels[0].Channel.Name != "terraform-now" {
		t.Errorf("top channel = %s, want the recent channel", channels[0].Channel.Name)
	}
}

func TestDecayDisabled(t *testing.T) {
	t.Parallel()
	ix := recencyIndex(0)
	people := ix.Search("terraform", 2)
	if len(people) != 2 {
		t.Fatalf("people = %d, want 2", len(people))
	}
	if people[0].Score != people[1].Score {
		t.Errorf("scores differ with decay off: %.4f vs %.4f", people[0].Score, people[1].Score)
	}
}

func TestDecayFactors(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.now = func() time.Time { return evalNow }
	tests := []struct {
		HalfLife time.Duration
		Time     time.Time
		WantMin  float64
		WantMax  float64
	}{{ // Test 0: Undated records never decay.
		HalfLife: DefaultHalfLife, WantMin: 1, WantMax: 1,
	}, { // Test 1: A future date clamps to one.
		HalfLife: DefaultHalfLife, Time: evalNow.AddDate(0, 0, 1), WantMin: 1, WantMax: 1,
	}, { // Test 2: One half-life halves the weight.
		HalfLife: DefaultHalfLife, Time: evalNow.Add(-DefaultHalfLife), WantMin: 0.499, WantMax: 0.501,
	}, { // Test 3: Two half-lives quarter it.
		HalfLife: DefaultHalfLife, Time: evalNow.Add(-2 * DefaultHalfLife), WantMin: 0.249, WantMax: 0.251,
	}, { // Test 4: Zero half-life disables decay.
		HalfLife: 0, Time: evalNow.AddDate(-3, 0, 0), WantMin: 1, WantMax: 1,
	}}
	for testNum, test := range tests {
		ix.SetHalfLife(test.HalfLife)
		got := ix.decay(test.Time)
		if got < test.WantMin || got > test.WantMax {
			t.Errorf("test %d: decay = %.4f, want in [%.3f, %.3f]",
				testNum, got, test.WantMin, test.WantMax)
		}
	}
}
