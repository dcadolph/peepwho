package cmd

import (
	"strings"
	"testing"
)

// TestJSONHumanContradiction verifies asking for JSON and human output in one
// run is an error instead of one flag silently winning.
func TestJSONHumanContradiction(t *testing.T) {
	t.Parallel()
	if _, _, err := runCmd(t, "--json", "--human", "version"); err == nil {
		t.Fatal("--json --human: want error, got nil")
	} else if !strings.Contains(err.Error(), "--human") {
		t.Errorf("error = %v, want it to name the contradiction", err)
	}
	if _, _, err := runCmd(t, "--pretty", "--human", "version"); err == nil {
		t.Fatal("--pretty --human: want error, got nil")
	}
	if _, _, err := runCmd(t, "--json", "version"); err != nil {
		t.Errorf("--json alone: %v", err)
	}
	if _, _, err := runCmd(t, "--human", "version"); err != nil {
		t.Errorf("--human alone: %v", err)
	}
}
