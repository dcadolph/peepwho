package connector

import (
	"fmt"
	"slices"
	"testing"
)

// TestPhraseTokens checks that adjacent words yield a hyphenated phrase beside
// the words themselves, so a compound subject accumulates its own weight.
func TestPhraseTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In       string
		WantHave []string
		WantMiss []string
	}{{ // Test 0: A two-word subject keeps its phrase and its words.
		In:       "billing retries",
		WantHave: []string{"billing", "retries", "billing-retries"},
	}, { // Test 1: Noise words are dropped before phrasing, so they never pair.
		In:       "Fix the kafka lag",
		WantHave: []string{"kafka", "lag", "kafka-lag"},
		WantMiss: []string{"fix-the", "the-kafka"},
	}, { // Test 2: A single surviving word yields no phrase.
		In:       "kubernetes",
		WantHave: []string{"kubernetes"},
		WantMiss: []string{"kubernetes-"},
	}, { // Test 3: Empty text yields nothing.
		In: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := phraseTokens(test.In)
			for _, want := range test.WantHave {
				if !slices.Contains(got, want) {
					t.Errorf("phraseTokens(%q) = %v, want it to contain %q", test.In, got, want)
				}
			}
			for _, bad := range test.WantMiss {
				if slices.Contains(got, bad) {
					t.Errorf("phraseTokens(%q) = %v, want it to omit %q", test.In, got, bad)
				}
			}
		})
	}
}
