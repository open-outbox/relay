// Package main provides the CLI entry point for the Open Outbox Relay.
// It uses the Cobra library to provide operational commands like 'prune' and 'status'.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := Execute(ctx); err != nil {
		os.Exit(1)
	}
}
