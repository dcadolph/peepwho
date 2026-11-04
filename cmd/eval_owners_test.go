package cmd

import (
	"errors"
	"fmt"
	"testing"
)

// TestEvalOwnersRefusesScoringFlagsWithoutAProbe pins that a scoring flag with
// nothing to score is refused. Both flags read the saved forge bytes, so
// without --probe they would change nothing and report a number
// indistinguishable from the unflagged one, which in a benchmark reads as a
// finding that the scoring did not help.
func TestEvalOwnersRefusesScoringFlagsWithoutAProbe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name string
		Args []string
		Want error
	}{{ // Test 0: A credit mode with no probe to apply it to is refused.
		Name: "credit without probe",
		Args: []string{"eval", "owners", "--repo", ".", "--credit", "answered"},
		Want: ErrBadArgs,
	}, { // Test 1: Answer-hood topics with no episodes to read is refused.
		Name: "answer topics without probe",
		Args: []string{"eval", "owners", "--repo", ".", "--answer-topics"},
		Want: ErrBadArgs,
	}, { // Test 2: The repository is still required first.
		Name: "no repo",
		Args: []string{"eval", "owners"},
		Want: ErrBadArgs,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			_, _, err := runCmd(t, test.Args...)
			if !errors.Is(err, test.Want) {
				t.Errorf("err = %v, want %v", err, test.Want)
			}
		})
	}
}
