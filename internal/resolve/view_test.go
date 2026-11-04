package resolve

import (
	"fmt"
	"testing"
)

func TestStrengthLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In         float64
		WantResult string
	}{{ // Test 0: Zero means unknown.
		In: 0, WantResult: "",
	}, { // Test 1: Full strength is strong.
		In: 1, WantResult: "strong",
	}, { // Test 2: The strong floor is inclusive.
		In: 0.75, WantResult: "strong",
	}, { // Test 3: Between floors is moderate.
		In: 0.5, WantResult: "moderate",
	}, { // Test 4: The moderate floor is inclusive.
		In: 0.45, WantResult: "moderate",
	}, { // Test 5: Below the moderate floor is weak.
		In: 0.2, WantResult: "weak",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := StrengthLabel(test.In); got != test.WantResult {
				t.Errorf("StrengthLabel(%.2f) = %q, want %q", test.In, got, test.WantResult)
			}
		})
	}
}
