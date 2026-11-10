package prompt

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestStyleHelpersPlain pins every wizard line shape with color off, which is
// what a piped or redirected run gets: exact indentation, markers, and streams.
func TestStyleHelpersPlain(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	p := New(strings.NewReader(""), &out, &errOut)

	p.Step("Pick a %s", "source")
	p.Detail("What this source adds.")
	p.Hint("Skip with enter.")
	p.Command("export WHOTOASK_SLACK_TOKEN=...")
	p.Success("saved")
	p.Blank()
	p.Warn("token looks short")
	p.Fail("could not reach %s", "slack")

	wantOut := "==> Pick a source\n" +
		"    What this source adds.\n" +
		"    Skip with enter.\n" +
		"        export WHOTOASK_SLACK_TOKEN=...\n" +
		"    ✓ saved\n" +
		"\n"
	if diff := cmp.Diff(wantOut, out.String()); diff != "" {
		t.Errorf("out mismatch (-want +got):\n%s", diff)
	}
	wantErr := "    ! token looks short\n" +
		"    ✗ could not reach slack\n"
	if diff := cmp.Diff(wantErr, errOut.String()); diff != "" {
		t.Errorf("errOut mismatch (-want +got):\n%s", diff)
	}
}

// TestStyleHelpersColor proves the ANSI paint wraps only when color is on, and
// that every painted run is closed with a reset.
func TestStyleHelpersColor(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	p := New(strings.NewReader(""), &out, &errOut)
	p.color = true

	p.Step("go")
	p.Success("done")
	p.Fail("broke")

	got := out.String() + errOut.String()
	for _, want := range []string{ansiBold, ansiBlue, ansiGreen, ansiRed} {
		if !strings.Contains(got, want) {
			t.Errorf("colored output missing %q", want)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		// A painted run must be closed on its own line, or color leaks into
		// everything printed after it.
		if strings.Contains(line, "\033[") && !strings.HasSuffix(strings.TrimRight(line, " "), ansiReset) &&
			!strings.Contains(line, ansiReset) {
			t.Errorf("line %q opens color and never resets", line)
		}
	}
}
