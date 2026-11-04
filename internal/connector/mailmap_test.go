package connector

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestMailmapResolve covers each .mailmap line form and lookup precedence.
func TestMailmapResolve(t *testing.T) {
	t.Parallel()
	const spec = "# comment\n" +
		"\n" +
		"<proper@x.com> <alias@x.com>\n" +
		"Proper Name <named@x.com> <plain@x.com>\n" +
		"Split Owner <split@x.com> Commit Alias <commit@x.com>\n" +
		"Sole Name <sole@x.com>\n"
	mm := parseMailmapSpec(t, spec)

	tests := []struct {
		Name      string
		InName    string
		InEmail   string
		WantName  string
		WantEmail string
	}{{ // Test 0: Email-only remap keeps the commit name, swaps the email.
		Name: "email only", InName: "Whoever", InEmail: "alias@x.com",
		WantName: "Whoever", WantEmail: "proper@x.com",
	}, { // Test 1: Named email form overrides both name and email by email match.
		Name: "named by email", InName: "Old", InEmail: "plain@x.com",
		WantName: "Proper Name", WantEmail: "named@x.com",
	}, { // Test 2: Split form applies only when the commit name also matches.
		Name: "split name matches", InName: "Commit Alias", InEmail: "commit@x.com",
		WantName: "Split Owner", WantEmail: "split@x.com",
	}, { // Test 3: Split form leaves a non-matching name on that email untouched.
		Name: "split name differs", InName: "Someone Else", InEmail: "commit@x.com",
		WantName: "Someone Else", WantEmail: "commit@x.com",
	}, { // Test 4: Sole-name form canonicalizes the name for its own email.
		Name: "sole name", InName: "s", InEmail: "SOLE@x.com",
		WantName: "Sole Name", WantEmail: "sole@x.com",
	}, { // Test 5: An unknown email passes through unchanged.
		Name: "unknown", InName: "Nobody", InEmail: "none@x.com",
		WantName: "Nobody", WantEmail: "none@x.com",
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			gotName, gotEmail := mm.resolve(test.InName, test.InEmail)
			if diff := cmp.Diff([]string{test.WantName, test.WantEmail},
				[]string{gotName, gotEmail}); diff != "" {
				t.Errorf("resolve mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// parseMailmapSpec builds a mailmap from inline text for tests.
func parseMailmapSpec(t *testing.T, spec string) mailmap {
	t.Helper()
	mm := make(mailmap)
	for line := range strings.SplitSeq(spec, "\n") {
		if proper, commit, ok := parseMailmapLine(line); ok {
			mm.add(commit, proper)
		}
	}
	return mm
}
