// Produces test events into the outbox table for local development and test
package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env file (ignores if missing, which is good for Prod/Docker)
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, using system environment variables")
	}

	// Load config with fallbacks
	dbURL := getEnv("STORAGE_URL", "postgres://postgres:postgres@localhost:5432/postgres")
	eventType := getEnv("LOCAL_TEST_TOPIC", "openoutbox.events.v1")
	batchSize, _ := strconv.Atoi(getEnv("LOCAL_PRODUCER_BATCH_SIZE", "1000"))

	intervalStr := getEnv("LOCAL_PRODUCER_INTERVAL", "1s")
	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		log.Printf("⚠️ Invalid duration '%s', defaulting to 1s", intervalStr)
		interval = time.Second
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("producer connection failed: %v", err)
	}
	defer pool.Close()

	log.Printf("open Outbox producer: Inserting %d '%s' events/sec", batchSize, eventType)

	for {
		batch := &pgx.Batch{}

		for i := 0; i < batchSize; i++ {
			userID := rand.Intn(10000)
			payload := fmt.Sprintf(
				`{"user_id": %d, "email": "user-%d@example.com"}`,
				userID,
				userID,
			)

			batch.Queue(
				`INSERT INTO openoutbox_events
				(event_id, event_type, partition_key, payload, headers, status)
				VALUES ($1, $2, $3, $4, $5, 'PENDING')`,
				uuid.New(),
				eventType,
				fmt.Sprintf("user-%d", userID),
				[]byte(payload),
				map[string]any{"trace_id": uuid.New().String(), "source": "load-test"},
			)
		}

		if err := pool.SendBatch(ctx, batch).Close(); err != nil {
			log.Printf("batch failed: %v", err)
		} else {
			log.Printf("inserted: %d events", batchSize)
		}

		time.Sleep(interval)
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
