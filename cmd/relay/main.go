// Package main is the entry point for the OpenOutbox Relay.
// It sets up signal handling, builds the dependency injection container,
// and manages the lifecycle of the relay engine and the HTTP server.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/open-outbox/relay/internal/container"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/telemetry"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// main initializes the application and starts the main execution loop.
func main() {
	godotenv.Load()
	if err := run(); err != nil {
		log.Fatalf("Relay terminated with error: %v", err)
	}
	log.Println("Relay process exited gracefully")
}

// Runs the relay engine and api servers
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c, err := container.BuildContainer(ctx)
	if err != nil {
		return fmt.Errorf("Failed to build container: %w", err)
	}

	return c.Invoke(
		func(
			engine *relay.Engine,
			api *relay.Server,
			logger *zap.Logger,
			tp *telemetry.OTelProviders,
		) error {

			defer func() { _ = logger.Sync() }()

			defer func() {
				logger.Info("Flushing telemetry data...")
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := tp.Shutdown(shutdownCtx); err != nil {
					logger.Error("OTel shutdown failed", zap.Error(err))
				}
			}()

			defer func() {
				if err := engine.Stop(); err != nil {
					logger.Error("Failed to stop engine cleanly", zap.Error(err))
				}
			}()

			g, groupCtx := errgroup.WithContext(ctx)
			g.Go(func() error {
				return api.Start(groupCtx)
			})
			g.Go(func() error {
				return engine.Start(groupCtx)
			})
			logger.Info("OpenOutbox Relay is running...")
			return g.Wait()
		},
	)
}
