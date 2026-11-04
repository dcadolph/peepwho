package connector

import (
	"fmt"
	"testing"
)

// TestIsBotAuthorNamesAutomation covers the automation accounts a real
// repository carries, including the ones whose names hold no "bot" word.
func TestIsBotAuthorNamesAutomation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name  string
		Email string
		Want  bool
	}{{ // Test 0: GitHub's coding agent, as it commits on prometheus.
		Name: "Copilot", Email: "198982749+Copilot@users.noreply.github.com", Want: true,
	}, { // Test 1: The same agent under a plain address.
		Name: "copilot", Email: "copilot@example.com", Want: true,
	}, { // Test 2: A GitHub App keeps its [bot] suffix.
		Name: "dependabot[bot]", Email: "49699333+dependabot[bot]@users.noreply.github.com",
		Want: true,
	}, { // Test 3: Actions, whose noreply login names it.
		Name: "github-actions", Email: "41898282+github-actions@users.noreply.github.com",
		Want: true,
	}, { // Test 4: A merge robot named as a word.
		Name: "PyTorch MergeBot", Email: "pytorchmergebot@users.noreply.github.com", Want: true,
	}, { // Test 5: A release robot, whose name carries no bot word at all.
		Name: "Kubernetes Release Robot", Email: "k8s-release-robot@users.noreply.github.com",
		Want: true,
	}, { // Test 6: A person whose name merely contains bot letters.
		Name: "Talbot Abbott", Email: "talbot@corp.com", Want: false,
	}, { // Test 7: A person sharing a surname with an automation word.
		Name: "Robin Copilot-Smith", Email: "robin@corp.com", Want: false,
	}, { // Test 8: An ordinary contributor.
		Name: "Julien Pivotto", Email: "291750+roidelapluie@users.noreply.github.com",
		Want: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := isBotAuthor(test.Name, test.Email); got != test.Want {
				t.Errorf("isBotAuthor(%q, %q) = %v, want %v",
					test.Name, test.Email, got, test.Want)
			}
		})
	}
}
