package render

import (
	"strconv"
	"strings"
)

// EscapeControls returns s with every terminal control character replaced by
// its visible escape, e.g. an ESC byte by the four characters \x1b. Names,
// comments, and topics come from sources third parties control, and a git
// author name or web comment carrying an escape sequence can retitle the
// window, hide output, or forge rows in the middle of a report. Escaping
// rather than stripping keeps the tampering visible, so what reaches the
// terminal is exactly what is on the screen.
func EscapeControls(s string) string {
	if !strings.ContainsFunc(s, isTerminalControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		if isTerminalControl(r) {
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isTerminalControl reports whether r can change terminal state rather than
// print: the C0 controls including newline and tab, DEL, and the C1 range
// some terminals honor as escape introducers.
func isTerminalControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// EscapeControlsList escapes every element of items, for the slices renderers
// join into one line.
func EscapeControlsList(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = EscapeControls(s)
	}
	return out
}
