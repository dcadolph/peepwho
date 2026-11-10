// Package cmd implements the whotoask command-line interface.
package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Execute runs the whotoask root command and returns any error to the caller. It
// installs one signal-aware context so an interrupt or termination cancels
// whatever is running, from a long index or ask to a long-lived serve or bot.
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return newRootCmd().ExecuteContext(ctx)
}
