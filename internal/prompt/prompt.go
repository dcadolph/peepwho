// Package prompt renders whotoask's interactive setup wizard: brew-style step
// headers plus the line, secret, confirm, and menu reads the connect command
// drives. Secrets are read without echo and never stored here; this package only
// moves a value from the terminal into memory for the caller to use and discard.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// ErrInputClosed reports that input ended before an answer was given, such as
// Ctrl+D or a closed pipe. It wraps io.EOF for callers that still test for it,
// but it is distinct so a network error that happens to unwrap to io.EOF is
// never mistaken for the user ending input.
var ErrInputClosed = fmt.Errorf("prompt: input closed: %w", io.EOF)

// ErrAborted reports that the user backed out of a prompt, for example by
// answering the menu with "q". Callers treat it as a clean, zero-status exit.
var ErrAborted = errors.New("prompt: aborted")

// IO is one interactive session bound to an input, an output, and an error
// stream, with color resolved once from the output.
type IO struct {
	// r reads input lines for every prompt.
	r *bufio.Reader
	// tty is the terminal backing the input, or nil when input is not a terminal.
	tty *os.File
	// out receives prompts and rendered wizard output.
	out io.Writer
	// errOut receives warnings and failures.
	errOut io.Writer
	// color enables ANSI styling on out.
	color bool
}

// New returns an IO reading from in and writing to out and errOut. Input is a
// terminal, enabling no-echo secret reads, only when in is an *os.File backed by
// one. Color is enabled only when out is a terminal and NO_COLOR is unset.
func New(in io.Reader, out, errOut io.Writer) *IO {
	p := &IO{r: bufio.NewReader(in), out: out, errOut: errOut}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		p.tty = f
	}
	p.color = colorEnabled(out)
	return p
}

// colorEnabled reports whether ANSI styling should be written to w. It honors
// the NO_COLOR convention and a dumb terminal, and requires w to be a terminal,
// so redirected or piped output stays plain.
func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// Interactive reports whether input is a terminal. The connect command refuses
// to run without one rather than blocking on a read that never arrives.
func (p *IO) Interactive() bool { return p.tty != nil }

// Line prompts with label and returns the entered text, trimmed.
func (p *IO) Line(label string) (string, error) {
	fmt.Fprintf(p.out, "%s%s: ", indent, label)
	return p.readLine()
}

// Secret prompts with label and reads a value without echoing it, so a token
// never appears on screen. It reads from the terminal when input is one, and
// otherwise falls back to a plain line read so scripted tests can drive it. The
// connect command guards against a non-terminal input before ever calling this.
func (p *IO) Secret(label string) (string, error) {
	fmt.Fprintf(p.out, "%s%s: ", indent, label)
	if p.tty == nil {
		return p.readLine()
	}
	b, err := term.ReadPassword(int(p.tty.Fd()))
	fmt.Fprintln(p.out)
	if err != nil {
		return "", fmt.Errorf("read secret: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Confirm prompts a yes/no question with label, returning def on an empty answer.
func (p *IO) Confirm(label string, def bool) (bool, error) {
	choices := "[y/N]"
	if def {
		choices = "[Y/n]"
	}
	fmt.Fprintf(p.out, "%s%s %s: ", indent, label, p.dim(choices))
	line, err := p.readLine()
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return def, nil
	}
}

// Choose lists options and returns the zero-based index the user picks. It
// re-asks on an out-of-range answer and returns ErrAborted when the user
// answers "q".
func (p *IO) Choose(label string, options []string) (int, error) {
	for i, o := range options {
		fmt.Fprintf(p.out, "%s%s %s\n", indent, p.accent(fmt.Sprintf("%2d.", i+1)), o)
	}
	for {
		fmt.Fprintf(p.out, "%s%s %s: ", indent, label, p.dim(fmt.Sprintf("[1-%d, q]", len(options))))
		line, err := p.readLine()
		if err != nil {
			return -1, err
		}
		if line == "q" || line == "Q" {
			return -1, ErrAborted
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		p.Warn("Enter a number from 1 to %d, or q to quit.", len(options))
	}
}

// readLine reads one line, trimming trailing newline and surrounding space. A
// sole EOF with buffered text returns that text; an empty EOF returns io.EOF so
// a closed input reads as an abort.
func (p *IO) readLine() (string, error) {
	s, err := p.r.ReadString('\n')
	s = strings.TrimSpace(strings.TrimRight(s, "\r\n"))
	if err != nil {
		if errors.Is(err, io.EOF) {
			if s != "" {
				return s, nil
			}
			return "", ErrInputClosed
		}
		return "", err
	}
	return s, nil
}
