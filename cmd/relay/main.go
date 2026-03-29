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
)

func main() {
	// 1. Setup global context for shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 2. Build the DI Container
	c := container.BuildContainer()

	err := c.Invoke(func(engine *relay.Engine, api *relay.Server, logger *zap.Logger) {
		defer logger.Sync()
		
		// Start API (Non-blocking)
		api.Start()

		log.Println("Relay Engine (DI) starting...")
		
		// Run Engine (Blocking)
		if err := engine.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("Engine error: %v", err)
		}

		// Graceful Shutdown for API
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = api.Stop(shutdownCtx)
	})

	if err != nil {
		log.Fatalf("Failed to start application: %v", err)
	}

	log.Println("Relay exited cleanly.")
}