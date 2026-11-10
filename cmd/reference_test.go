package cmd

import (
	"os"
	"strings"
	"testing"
)

// TestEveryCommandIsDocumented holds the reference to its own claim. It opens
// with "every command, flag, source, and environment variable", and a command
// that ships without a section quietly makes that false: the people most likely
// to read the reference are the ones who cannot ask anybody.
func TestEveryCommandIsDocumented(t *testing.T) {
	t.Parallel()
	doc, err := os.ReadFile("../docs/REFERENCE.md")
	if err != nil {
		t.Fatalf("read reference: %v", err)
	}
	reference := string(doc)

	// Cobra generates these two, and they document themselves.
	generated := map[string]bool{"help": true, "completion": true}

	for _, sub := range newRootCmd().Commands() {
		name := sub.Name()
		if generated[name] || sub.Hidden {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(reference, "## whotoask "+name+"\n") {
				t.Errorf("docs/REFERENCE.md has no `## whotoask %s` section", name)
			}
		})
	}
}
