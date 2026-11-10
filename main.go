// Command whotoask locates the right person or channel for a question.
//
// It indexes people, teams, and topics from work sources, then answers
// "who do I talk to about X" in plain language. Two engines back it: a non-LLM
// keyword ranker and an optional local LLM. Indexed data stays on the machine
// unless an explicit egress policy permits otherwise.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dcadolph/whotoask/cmd"
)

// interruptCode is the conventional exit status for a process ended by an
// interrupt: 128 plus the signal number, SIGINT being 2.
const interruptCode = 130

// main runs the whotoask CLI and maps any error to a non-zero exit code. An
// interrupt is a deliberate stop, not a failure, so it exits quietly with the
// conventional code rather than printing the half-finished operation's error.
func main() {
	if err := cmd.Execute(); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "whotoask: canceled")
			os.Exit(interruptCode)
		}
		fmt.Fprintln(os.Stderr, "whotoask:", err)
		os.Exit(1)
	}
}
