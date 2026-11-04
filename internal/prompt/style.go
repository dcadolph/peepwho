package prompt

import "fmt"

// ANSI select-graphic-rendition codes for the brew-style output. They are
// emitted only when color is enabled; otherwise every helper writes plain text.
const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiBlue   = "\033[34m"
)

// indent aligns body lines under a step header.
const indent = "    "

// markSuccess and markFail prefix confirmation and failure lines. They are
// dingbats, not emoji, so they render as plain text on any terminal.
const (
	markSuccess = "✓"
	markFail    = "✗"
)

// paint wraps s in code and a reset when color is enabled, else returns s.
func (p *IO) paint(code, s string) string {
	if !p.color {
		return s
	}
	return code + s + ansiReset
}

// bold renders s in bold.
func (p *IO) bold(s string) string { return p.paint(ansiBold, s) }

// dim renders s in a muted style for the least important text.
func (p *IO) dim(s string) string { return p.paint(ansiDim, s) }

// accent renders s in the brand accent, blue in a terminal.
func (p *IO) accent(s string) string { return p.paint(ansiBlue, s) }

// accentBold renders s in bold blue, used for the step arrow.
func (p *IO) accentBold(s string) string {
	if !p.color {
		return s
	}
	return ansiBold + ansiBlue + s + ansiReset
}

// Blank writes an empty line to separate steps.
func (p *IO) Blank() { fmt.Fprintln(p.out) }

// Step writes a brew-style step header: a bold accent arrow and a bold message.
func (p *IO) Step(format string, a ...any) {
	fmt.Fprintf(p.out, "%s %s\n", p.accentBold("==>"), p.bold(fmt.Sprintf(format, a...)))
}

// Detail writes an indented body line in the default weight, so descriptions
// stay readable rather than washed out.
func (p *IO) Detail(format string, a ...any) {
	fmt.Fprintf(p.out, "%s%s\n", indent, fmt.Sprintf(format, a...))
}

// Hint writes an indented line in a muted style for secondary asides.
func (p *IO) Hint(format string, a ...any) {
	fmt.Fprintf(p.out, "%s%s\n", indent, p.dim(fmt.Sprintf(format, a...)))
}

// Command writes an indented, accented line for a command the user copies.
func (p *IO) Command(format string, a ...any) {
	fmt.Fprintf(p.out, "%s%s%s\n", indent, indent, p.accent(fmt.Sprintf(format, a...)))
}

// Success writes an indented confirmation line marked in green.
func (p *IO) Success(format string, a ...any) {
	fmt.Fprintf(p.out, "%s%s %s\n", indent, p.paint(ansiGreen, markSuccess), fmt.Sprintf(format, a...))
}

// Warn writes an indented warning line marked in yellow, to the error stream.
func (p *IO) Warn(format string, a ...any) {
	fmt.Fprintf(p.errOut, "%s%s %s\n", indent, p.paint(ansiYellow, "!"), fmt.Sprintf(format, a...))
}

// Fail writes an indented failure line marked in red, to the error stream.
func (p *IO) Fail(format string, a ...any) {
	fmt.Fprintf(p.errOut, "%s%s %s\n", indent, p.paint(ansiRed, markFail), fmt.Sprintf(format, a...))
}
