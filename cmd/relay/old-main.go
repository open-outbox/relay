package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/open-outbox/relay/internal/config"
	"github.com/open-outbox/relay/internal/publishers"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/storage"
)

func main() {
	// 1. Create a context that listens for the interrupt signal (Ctrl+C)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
    if err != nil {
        log.Fatalf("Failed to load config: %v", err)
    }

    log.Printf("Config Loaded: BatchSize=%d, Interval=%v", cfg.BatchSize, cfg.PollInterval)

	log.Println("Relay starting... Press Ctrl+C to stop.")

	// memDB := storage.NewMemory()
    // // Seed 3 initial events
    // memDB.Add(relay.Event{ID: uuid.New(), Topic: "test.1", Payload: []byte("First")})
    // memDB.Add(relay.Event{ID: uuid.New(), Topic: "test.2", Payload: []byte("Second")})
    // memDB.Add(relay.Event{ID: uuid.New(), Topic: "test.3", Payload: []byte("Third")})

	// 2. Connect to Postgres
	connStr := "postgres://postgres:postgres@localhost:5432/postgres"
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v", err)
	}
	defer pool.Close()
	dbStorage := storage.NewPostgres(pool)

    // cliPub := publishers.NewStdout()
	// 3. Initialize NATS instead of Stdout
    natsPub, err := publishers.NewNats(nats.DefaultURL) // "nats://localhost:4222"
    if err != nil {
        log.Fatalf("NATS error: %v", err)
    }
	log.Println("Connected to nats...")
	defer natsPub.Close()

	api := relay.NewServer(dbStorage, cfg.ServerPort)
	api.Start()

	engine := relay.NewEngine(dbStorage, natsPub, cfg.PollInterval)
	// 6. Start the Engine (BLOCKING)
	// This function stays here until 'ctx' is canceled (via Ctrl+C)
	log.Println("Relay Engine running... [Press Ctrl+C to stop]")
	if err := engine.Run(ctx); err != nil {
		// We only log if the error isn't a normal cancellation
		if err != context.Canceled && err != context.DeadlineExceeded {
			log.Printf("Engine stopped with unexpected error: %v", err)
		}
	}
	
	/// 7. Graceful Shutdown sequence
	// Once we reach here, the engine has already stopped.
	// Now we give the API server 5 seconds to finish any active requests.
	log.Println("Initiating graceful shutdown...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := api.Stop(shutdownCtx); err != nil {
		log.Printf("API shutdown error: %v", err)
	}

	log.Println("Relay process exited cleanly.")
}