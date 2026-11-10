package cmd

import (
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/feedback"
	"github.com/dcadolph/whotoask/internal/render"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// TestRenderEscapesIndexedText drives renderers with hostile indexed and
// user-supplied text, proving no raw control byte reaches the writer. The
// style is plain, so any escape byte in the output came from the data. The
// comment path matters most: it arrives through the web feedback endpoint,
// written by whoever can reach the page.
func TestRenderEscapesIndexedText(t *testing.T) {
	t.Parallel()
	hostile := "Jo\x1b]0;owned\x07\x1b[8m"

	var b strings.Builder
	render.RenderFeedback(&b, []feedback.Entry{{
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
	render.RenderRisk(&b, []resolve.TopicRisk{{
		Topic: "billing", Level: "critical", BusFactor: 1, Concentration: 1,
		Experts: []resolve.RiskExpert{{Name: hostile, Share: 1}},
	}}, style{})
	if got := b.String(); strings.ContainsRune(got, '\x1b') {
		t.Errorf("renderRisk let a raw escape through: %q", got)
	}
}
