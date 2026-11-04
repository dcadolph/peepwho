package util

import (
	"fmt"
	"testing"
)

// TestSameFamily pins what counts as one name seen twice: containment either
// way, a separator-split word matching the other side whole, and nothing else.
func TestSameFamily(t *testing.T) {
	t.Parallel()
	tests := []struct {
		A, B string
		Want bool
	}{
		{"zwave", "zwave_js", true},             // Test 0: Containment, a inside b.
		{"zwave_js", "zwave", true},             // Test 1: Containment, b inside a.
		{"billing", "billing", true},            // Test 2: Identical names contain each other.
		{"config-flow", "flow", true},           // Test 3: Hyphen segment matches whole.
		{"js_zwave", "zwave", true},             // Test 4: Underscore segment matches whole.
		{"billing", "kafka", false},             // Test 5: Unrelated names.
		{"billing-retries", "kafka-lag", false}, // Test 6: Unrelated compounds.
		{"ingest", "invest", false},             // Test 7: Shared prefix is not family.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := SameFamily(test.A, test.B); got != test.Want {
				t.Errorf("SameFamily(%q, %q) = %v, want %v", test.A, test.B, got, test.Want)
			}
		})
	}
}
