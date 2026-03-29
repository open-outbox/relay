package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	// Tip: Use an env var for the connection string in 2026!
	connStr := "postgres://postgres:postgres@localhost:5432/postgres"

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		log.Fatalf("Producer failed to connect: %v", err)
	}
	defer pool.Close()

	batchSize := 10000

	log.Printf("OpenOutbox Producer started! Inserting %d events per batch...\n", batchSize)

	for {
		// Use a Batch to send all 100 inserts in one network trip
		batch := &pgx.Batch{}

		for i := 0; i < batchSize; i++ {
			id := uuid.New()
			userID := rand.Intn(10000)
			eventType := "user.signup"
			payload := fmt.Sprintf(`{"user_id": %d, "email": "user-%d@example.com"}`, userID, userID)

			// We use the UserID as the PartitionKey to ensure
			// all events for 'User 123' stay in order.
			partitionKey := fmt.Sprintf("user-%d", userID)

			// Simple headers for tracing simulation
			headers := map[string]interface{}{
				"trace_id": uuid.New().String(),
				"source":   "load-test-script",
			}

			// Aligning with your updated SQL schema
			query := `
				INSERT INTO outbox_events 
				(event_id, event_type, partition_key, payload, headers, status, available_at) 
				VALUES ($1, $2, $3, $4, $5, 'PENDING', NOW())`

			batch.Queue(query, id, eventType, partitionKey, []byte(payload), headers)
		}

		// Send the batch
		results := pool.SendBatch(ctx, batch)

		// We must close the results to check for errors and free the connection
		if err := results.Close(); err != nil {
			log.Printf("Batch Insert failed: %v", err)
		} else {
			log.Printf("Successfully inserted a batch of %d events at %s", batchSize, time.Now().Format(time.Kitchen))
		}

		// Wait 1 second before the next burst
		time.Sleep(1 * time.Second)
	}
}
