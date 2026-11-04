package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/peepwho/internal/feedback"
	"github.com/dcadolph/peepwho/internal/resolve"
)

// TestEscapeControls checks terminal control characters are made visible
// instead of reaching the terminal, and that clean strings pass unchanged.
func TestEscapeControls(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{{ // Test 0: A clean name passes through untouched.
		In: "Jane Roe", Want: "Jane Roe",
	}, { // Test 1: An OSC title-set plus conceal, the shape of a git author
		// name that retitles the window and hides output mid-assessment.
		In:   "Jo\x1b]0;pwned\x07\x1b[8m",
		Want: `Jo\x1b]0;pwned\a\x1b[8m`,
	}, { // Test 2: Newline and carriage return cannot forge extra rows.
		In: "top\nfake row\r", Want: `top\nfake row\r`,
	}, { // Test 3: DEL and a C1 CSI, which some terminals honor as escapes.
		In: "a\x7fbc", Want: `a\x7fbc`,
	}, { // Test 4: Non-ASCII text that is not a control survives as itself.
		In: "Bücher · naïve", Want: "Bücher · naïve",
	}, { // Test 5: A C1 control, the 8-bit CSI, is escaped too.
		In: "x\u009by", Want: `x\u009by`,
	}, { // Test 6: Empty input stays empty.
		In: "", Want: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := escapeControls(test.In)
			if diff := cmp.Diff(test.Want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
			if strings.ContainsFunc(got, isTerminalControl) {
				t.Errorf("escaped output still carries a control character: %q", got)
			}
		})
	}
}

// TestRenderEscapesIndexedText drives renderers with hostile indexed and
// user-supplied text, proving no raw control byte reaches the writer. The
// style is plain, so any escape byte in the output came from the data. The
// comment path matters most: it arrives through the web feedback endpoint,
// written by whoever can reach the page.
func TestRenderEscapesIndexedText(t *testing.T) {
	t.Parallel()
	hostile := "Jo\x1b]0;owned\x07\x1b[8m"

	var b strings.Builder
	renderFeedback(&b, []feedback.Entry{{
		Query: "who owns billing", Person: hostile, Vote: feedback.Helpful,
		Comment: "great\x1b[2Janswer",
	}}, style{})
	got := b.String()
	if strings.ContainsRune(got, '\x1b') {
		t.Errorf("renderFeedback let a raw escape through: %q", got)
	}
	if !strings.Contains(got, `\x1b`) {
		t.Errorf("renderFeedback dropped the evidence instead of escaping it: %q", got)
	}

	b.Reset()
	renderRisk(&b, []resolve.TopicRisk{{
		Topic: "billing", Level: "critical", BusFactor: 1, Concentration: 1,
		Experts: []resolve.RiskExpert{{Name: hostile, Share: 1}},
	}}, style{})
	if got := b.String(); strings.ContainsRune(got, '\x1b') {
		t.Errorf("renderRisk let a raw escape through: %q", got)
	}
}
