package cmd

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// style carries whether ANSI coloring is on. It is on only when writing to a
// terminal, so the same rendering code produces plain text in a pipe and
// colored text for a person, with no branching at each call site.
type style struct {
	// on reports whether ANSI escapes are emitted.
	on bool
}

// bold renders v bold at a terminal, plain otherwise.
func (s style) bold(v string) string { return s.wrap("1", v) }

// dim renders v faint at a terminal, plain otherwise.
func (s style) dim(v string) string { return s.wrap("2", v) }

// accent renders v in the signal color at a terminal, plain otherwise.
func (s style) accent(v string) string { return s.wrap("38;5;42", v) }

// warn renders v in a warning color at a terminal, plain otherwise.
func (s style) warn(v string) string { return s.wrap("33", v) }

// bad renders v in a red at a terminal, plain otherwise.
func (s style) bad(v string) string { return s.wrap("31", v) }

// wrap surrounds v with the ANSI code when coloring is on.
func (s style) wrap(code, v string) string {
	if !s.on || v == "" {
		return v
	}
	return "\033[" + code + "m" + v + "\033[0m"
}

// pad right-pads a possibly-colored cell to a visible width, measuring the raw
// text so ANSI escapes do not throw the alignment off.
func pad(colored, raw string, width int) string {
	if n := len([]rune(raw)); n < width {
		return colored + strings.Repeat(" ", width-n)
	}
	return colored
}

// isTerminal reports whether w writes to an interactive terminal.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// render writes v as human-readable text when the destination is a terminal,
// and as JSON when the output is piped or when --json or --pretty asks for it.
// --human forces the human form even through a pipe, for reading in a pager.
// The human function is handed a style that colors only at a real terminal.
func (o *options) render(w io.Writer, v any, human func(io.Writer, style)) error {
	if o.jsonOut || o.pretty {
		return writeJSON(w, v, o.pretty)
	}
	tty := isTerminal(w)
	if o.humanOut || tty {
		human(w, style{on: tty})
		return nil
	}
	return writeJSON(w, v, false)
}

// notForLine closes every human rendering of a knowledge-risk finding.
const notForLine = "Findings describe knowledge and systems at risk. " +
	"They are not for performance or employment decisions."

// renderNotFor writes notForLine as the last line of a finding.
func renderNotFor(w io.Writer, s style) {
	fmt.Fprintf(w, "\n%s\n", s.dim(notForLine))
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

// color256 wraps v in an xterm 256-color foreground when coloring is on.
func (s style) color256(code int, v string) string {
	return s.wrap("38;5;"+strconv.Itoa(code), v)
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

// confBar renders a zero-to-one confidence as a ten-segment bar, filled in the
// accent color and empty in a faint one.
func (s style) confBar(c float64) string {
	const n = 10
	f := int(c*float64(n) + 0.5)
	if f > n {
		f = n
	}
	if f < 0 {
		f = 0
	}
	return s.accent(strings.Repeat("\u25b0", f)) + s.dim(strings.Repeat("\u25b1", n-f))
}
