package render

import (
	"fmt"
	"strconv"
	"strings"
)

// Style carries whether ANSI coloring is on. It is on only when writing to a
// terminal, so the same rendering code produces plain text in a pipe and
// colored text for a person, with no branching at each call site.
type Style struct {
	// On reports whether ANSI escapes are emitted.
	On bool
}

// Bold renders v bold at a terminal, plain otherwise.
func (s Style) Bold(v string) string { return s.Wrap("1", v) }

// Dim renders v faint at a terminal, plain otherwise.
func (s Style) Dim(v string) string { return s.Wrap("2", v) }

// Accent renders v in the signal color at a terminal, plain otherwise.
func (s Style) Accent(v string) string { return s.Wrap("38;5;42", v) }

// Warn renders v in a warning color at a terminal, plain otherwise.
func (s Style) Warn(v string) string { return s.Wrap("33", v) }

// Bad renders v in a red at a terminal, plain otherwise.
func (s Style) Bad(v string) string { return s.Wrap("31", v) }

// Wrap surrounds v with the ANSI code when coloring is on.
func (s Style) Wrap(code, v string) string {
	if !s.On || v == "" {
		return v
	}
	return "\033[" + code + "m" + v + "\033[0m"
}

// Color256 wraps v in an xterm 256-color foreground when coloring is on.
func (s Style) Color256(code int, v string) string {
	return s.Wrap("38;5;"+strconv.Itoa(code), v)
}

// ConfBar renders a zero-to-one confidence as a ten-segment bar, filled in the
// accent color and empty in a faint one.
func (s Style) ConfBar(c float64) string {
	const n = 10
	f := int(c*float64(n) + 0.5)
	if f > n {
		f = n
	}
	if f < 0 {
		f = 0
	}
	return s.Accent(strings.Repeat("▰", f)) + s.Dim(strings.Repeat("▱", n-f))
}

// pad right-pads a possibly-colored cell to a visible width, measuring the raw
// text so ANSI escapes do not throw the alignment off.
func pad(colored, raw string, width int) string {
	if n := len([]rune(raw)); n < width {
		return colored + strings.Repeat(" ", width-n)
	}
	return colored
}

// pct renders a zero-to-one confidence as a right-aligned percentage, or a
// tilde when there is nothing to show.
func pct(c float64) string {
	if c <= 0 {
		return "   ~"
	}
	return fmt.Sprintf("%3.0f%%", c*100)
}

// joinRole combines a job title and a team into one label.
func joinRole(title, team string) string {
	switch {
	case title != "" && team != "":
		return title + ", " + team
	case title != "":
		return title
	default:
		return team
	}
}

// sourceColor returns a source's display label and 256-color code, echoing the
// colors the web UI uses so a source reads the same everywhere. Unknown sources
// get a neutral gray.
func sourceColor(src string) (label string, code int) {
	switch strings.ToLower(src) {
	case "github":
		return "GitHub", 141
	case "slack":
		return "Slack", 197
	case "pagerduty":
		return "PagerDuty", 41
	case "jira":
		return "Jira", 39
	case "confluence":
		return "Confluence", 33
	case "gitlab":
		return "GitLab", 208
	case "teams":
		return "Teams", 104
	case "linear":
		return "Linear", 99
	case "notion":
		return "Notion", 250
	case "":
		return "source", 245
	default:
		return src, 245
	}
}
