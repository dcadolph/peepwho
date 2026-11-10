package cmd

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/dcadolph/whotoask/internal/render"
)

// style is the render package's color switch, kept under its short name for
// the command callbacks that receive it.
type style = render.Style

// plural returns "s" unless n is one.
func plural(n int) string { return render.Plural(n) }

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
		human(w, style{On: tty})
		return nil
	}
	return writeJSON(w, v, false)
}

// notForLine closes every human rendering of a knowledge-risk finding.
const notForLine = "Findings describe knowledge and systems at risk. " +
	"They are not for performance or employment decisions."

// renderNotFor writes notForLine as the last line of a finding.
func renderNotFor(w io.Writer, s style) {
	fmt.Fprintf(w, "\n%s\n", s.Dim(notForLine))
}
