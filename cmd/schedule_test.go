package cmd

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestLaunchdPlist(t *testing.T) {
	t.Parallel()
	p := launchdPlist("dev.peepwho.refresh", "/usr/local/bin/peepwho", "/data", "/logs/r.log")
	wants := []string{
		"dev.peepwho.refresh", "/usr/local/bin/peepwho", "<string>refresh</string>",
		"<string>/data</string>", "/logs/r.log", "StartCalendarInterval",
	}
	for _, want := range wants {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

// TestLaunchdPlistEscapesXML checks a path holding XML-special characters
// still renders a well-formed plist. Unescaped, an '&' produced an invalid
// file whose only symptom was a cryptic launchctl failure while --status kept
// reporting the agent installed.
func TestLaunchdPlistEscapesXML(t *testing.T) {
	t.Parallel()
	dir := "/Users/d/Library/Mobile Documents/R&D <beta>/peepwho"
	p := launchdPlist("dev.peepwho.refresh", "/usr/local/bin/peepwho", dir, "/logs/r.log")

	if !strings.Contains(p, "R&amp;D &lt;beta&gt;") {
		t.Errorf("plist does not escape the path: %s", p)
	}
	// The whole document must stay parseable XML, not merely escape one value.
	dec := xml.NewDecoder(strings.NewReader(p))
	var value string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if cd, ok := tok.(xml.CharData); ok && strings.Contains(string(cd), "R&D") {
			value = string(cd)
		}
	}
	if value != dir {
		t.Errorf("decoded value = %q, want the original path %q", value, dir)
	}
}

func TestCountTrue(t *testing.T) {
	t.Parallel()
	if got := countTrue(true, false, true); got != 2 {
		t.Errorf("countTrue = %d, want 2", got)
	}
	if got := countTrue(false, false); got != 0 {
		t.Errorf("countTrue = %d, want 0", got)
	}
}
