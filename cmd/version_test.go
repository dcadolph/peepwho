package cmd

import "testing"

// TestVersionPrecedence pins the two ways a version arrives. A release binary
// is stamped by the linker and that value must survive; a `go install` binary
// gets no stamp and falls back to the module version the toolchain recorded,
// because reporting "dev" makes a good install look unfinished.
func TestVersionPrecedence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Stamped, Module, Want string
	}{
		{"1.2.3", "0.43.0", "1.2.3"}, // Test 0: the linker always wins.
		{"dev", "0.43.0", "0.43.0"},  // Test 1: go install reports its module.
		{"dev", "", "dev"},           // Test 2: a local build stays dev.
	}
	for testNum, test := range tests {
		if got := resolveVersion(test.Stamped, test.Module); got != test.Want {
			t.Errorf("test %d: resolveVersion(%q, %q) = %q, want %q",
				testNum, test.Stamped, test.Module, got, test.Want)
		}
	}
}
