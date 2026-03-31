// Package main is the entry point for the OpenOutbox Relay.
// It sets up signal handling, builds the dependency injection container,
// and manages the lifecycle of the relay engine and the HTTP server.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/open-outbox/relay/internal/container"
	"github.com/open-outbox/relay/internal/relay"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// main initializes the application and starts the main execution loop.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := container.BuildContainer(ctx)

	err := c.Invoke(func(engine *relay.Engine, api *relay.Server, logger *zap.Logger) error {
		defer func() { _ = logger.Sync() }()

		g, groupCtx := errgroup.WithContext(ctx)
		g.Go(func() error {
			return api.Start()
		})
		g.Go(func() error {
			return engine.Start(groupCtx)
		})

		g.Go(func() error {
			<-groupCtx.Done()
			logger.Info("Shutdown signal received, closing API...")

			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return api.Stop(shutdownCtx)
		})

		logger.Info("OpenOutbox Relay is running...")
		return g.Wait()
	})

	if err != nil {
		log.Fatalf("Relay terminated with error: %v", err)
	}
	log.Println("Relay process exited gracefully")
}
