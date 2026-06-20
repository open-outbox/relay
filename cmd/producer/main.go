// Produces test events into the outbox table for local development and test
package main

import (
	"context"
	"crypto/rand" // Using crypto/rand as requested
	"fmt"
	"log"
	"math/big"
	math_rand "math/rand/v2" // Using newer math/rand/v2 for the partition keys
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/open-outbox/relay/internal/config"
	"github.com/spf13/cobra"
)

var (
	dbURL       string
	table       string
	topic       string
	batchSize   int
	interval    time.Duration
	seedCount   int
	payloadSize int
)

var rootCmd = &cobra.Command{
	Use:   "producer",
	Short: "Open Outbox Benchmark Producer",
}

func init() {
	_ = godotenv.Load()

	rootCmd.PersistentFlags().
		StringVar(
			&dbURL,
			"storage-url",
			os.Getenv("STORAGE_URL"),
			"Database URL (Default: STORAGE_URL)",
		)
	rootCmd.PersistentFlags().
		StringVar(
			&table,
			"table-name",
			getEnv("STORAGE_TABLE_NAME",
				config.DefaultTableName),
			"Table Name",
		)

	rootCmd.PersistentFlags().
		StringVar(
			&topic,
			"topic",
			getEnv("LOCAL_TEST_TOPIC", "openoutbox.events.v1"),
			"Target topic",
		)
	rootCmd.PersistentFlags().
		IntVar(
			&batchSize,
			"batch-size",
			getEnvInt("LOCAL_PRODUCER_BATCH_SIZE", 100),
			"Batch size",
		)
	rootCmd.PersistentFlags().
		DurationVar(
			&interval,
			"interval",
			getEnvDuration("LOCAL_PRODUCER_INTERVAL", 1*time.Second),
			"Interval between batches",
		)
	rootCmd.PersistentFlags().
		IntVar(
			&seedCount,
			"count",
			getEnvInt("LOCAL_PRODUCER_SEED_COUNT", 100000),
			"Total events to produce for seed command",
		)
	rootCmd.PersistentFlags().
		IntVar(
			&payloadSize,
			"payload-size",
			getEnvInt("LOCAL_PRODUCER_PAYLOAD_SIZE", 150),
			"Size of the random payload in bytes",
		)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

var seedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Inject a fixed number of events and exit",
	RunE: func(cmd *cobra.Command, _ []string) error {
		pool, err := pgxpool.New(cmd.Context(), dbURL)
		if err != nil {
			return err
		}
		defer pool.Close()

		staticPayload := generateStaticPayload(payloadSize)

		fmt.Printf(
			"🚀 Seeding %d events (%d bytes each) into %s...\n",
			seedCount,
			payloadSize,
			table,
		)
		start := time.Now()

		for produced := 0; produced < seedCount; {
			toSend := batchSize
			if remaining := seedCount - produced; remaining < batchSize {
				toSend = remaining
			}

			if err := sendBatch(cmd.Context(), pool, toSend, staticPayload); err != nil {
				return err
			}

			produced += toSend
			fmt.Printf("\rInserted: %d/%d", produced, seedCount)
		}

		fmt.Printf(
			"\n✅ Done in %v (Avg: %.0f eps)\n",
			time.Since(start),
			float64(seedCount)/time.Since(start).Seconds(),
		)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(seedCmd)
}

var benchCmd = &cobra.Command{
	Use:   "bench",
	Short: "Produce events continuously at a set interval",
	RunE: func(cmd *cobra.Command, _ []string) error {
		pool, err := pgxpool.New(cmd.Context(), dbURL)
		if err != nil {
			return err
		}
		defer pool.Close()

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		staticPayload := generateStaticPayload(payloadSize)

		fmt.Printf(
			"🔥 Benchmark mode active: %d events (%d bytes) every %v\n",
			batchSize,
			payloadSize,
			interval,
		)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				fmt.Println("\nStopping benchmark mode...")
				return nil
			case <-ticker.C:
				if err := sendBatch(ctx, pool, batchSize, staticPayload); err != nil {
					log.Printf("Batch error: %v", err)
				}
				fmt.Printf("%d events inserted\n", batchSize)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(benchCmd)
}

func sendBatch(
	ctx context.Context,
	pool *pgxpool.Pool,
	currentSize int,
	payload []byte, // Accept the pre-generated payload
) error {
	query := fmt.Sprintf(`
        INSERT INTO %s (event_id, event_type, partition_key, payload, headers, status)
        VALUES ($1, $2, $3, $4, $5, 'PENDING')`, table)

	batch := &pgx.Batch{}
	for i := 0; i < currentSize; i++ {
		select {
		case <-ctx.Done():
			fmt.Println("Producer received shutdown signal, stopping batch...")
			return ctx.Err()
		default:
			userID := math_rand.IntN(100000)

			batch.Queue(
				query,
				uuid.New(),
				topic,
				fmt.Sprintf("user-%d", userID),
				payload,
				map[string]any{"trace_id": uuid.New().String()},
			)
		}
	}

	return pool.SendBatch(ctx, batch).Close()
}

const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func generateStaticPayload(size int) []byte {
	b := make([]byte, size)
	madIndex := big.NewInt(int64(len(charset)))

	for i := 0; i < size; i++ {
		idx, err := rand.Int(rand.Reader, madIndex)
		if err != nil {
			log.Fatalf("failed to generate secure random index: %v", err)
		}
		b[i] = charset[idx.Int64()]
	}
	return b
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	val, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	i, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}
	return i
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	val, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		return fallback
	}
	return d
}
