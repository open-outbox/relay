//go:build integration

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	dockercontainer "github.com/moby/moby/api/types/container"
	nats_go "github.com/nats-io/nats.go"
	"github.com/open-outbox/relay/internal/relay"
	redis_go "github.com/redis/go-redis/v9"
	kafka_go "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/kafka"
	"github.com/testcontainers/testcontainers-go/modules/nats"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

type SeedOptions struct {
	ID          uuid.UUID
	Status      relay.EventStatus
	CreatedAt   time.Time
	AvailableAt time.Time
	DeliveredAt *time.Time
	UpdatedAt   *time.Time
	LockedAt    *time.Time
	LockedBy    *string
	Attempts    int
}

func ptr[T any](v T) *T {
	return &v
}

type EventSeeder interface {
	Seed(opts SeedOptions)
}

type PostgresSeeder struct {
	t         *testing.T
	ctx       context.Context
	pool      *pgxpool.Pool
	tableName string
}

func (s *PostgresSeeder) Seed(opts SeedOptions) {
	seedPostgres(s.ctx, opts, s.pool, s.tableName)
}

func setupTestPostgres(t *testing.T) (*sql.DB, string) {
	t.Helper()
	ctx := context.Background()

	schemaPath, err := filepath.Abs("../../schema/postgres/open-outbox.sql")
	require.NoError(t, err)

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("outbox_db"),
		postgres.WithUsername("user"),
		postgres.WithPassword("pass"),
		postgres.WithInitScripts(schemaPath),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(20*time.Second)),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		pgContainer.Terminate(context.Background())
	})

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := sql.Open("pgx", connStr)
	require.NoError(t, err)

	return db, connStr
}

func setupNats(t *testing.T) (*nats_go.Conn, string) {
	t.Helper()
	ctx := context.Background()

	natsContainer, err := nats.Run(ctx, "nats:2.10-alpine",
		testcontainers.WithWaitStrategy(wait.ForLog("Server is ready")),
		testcontainers.WithConfigModifier(func(conf *dockercontainer.Config) {
			conf.Cmd = []string{"-js"}
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { natsContainer.Terminate(context.Background()) })

	url, _ := natsContainer.ConnectionString(ctx)
	nc, err := nats_go.Connect(url)
	require.NoError(t, err)

	return nc, url
}

func setupRedis(t *testing.T) (*redis_go.Client, string) {
	t.Helper()
	ctx := context.Background()

	// Start Redis Container
	redisContainer, err := redis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err)

	// Ensure cleanup
	t.Cleanup(func() {
		if err := redisContainer.Terminate(context.Background()); err != nil {
			t.Logf("failed to terminate container: %v", err)
		}
	})

	endpoint, err := redisContainer.Endpoint(ctx, "")
	require.NoError(t, err)

	// Format URL for the publisher
	redisUrl := "redis://" + endpoint

	// Create a client for the test to verify results
	opts, err := redis_go.ParseURL(redisUrl)
	require.NoError(t, err)

	client := redis_go.NewClient(opts)

	// Quick ping to ensure it's ready
	require.NoError(t, client.Ping(ctx).Err())

	return client, redisUrl
}

func setupKafka(t *testing.T, topic string) string {
	t.Helper()
	ctx := context.Background()

	// The official module handles the "Advertised Listener" logic for you
	kafkaContainer, err := kafka.Run(ctx, "confluentinc/confluent-local:7.5.0")
	require.NoError(t, err)

	t.Cleanup(func() {
		kafkaContainer.Terminate(ctx)
	})

	// This returns the correctly mapped localhost:PORT
	brokers, err := kafkaContainer.Brokers(ctx)
	require.NoError(t, err)
	broker := brokers[0]

	// Create topic
	createKafkaTopic(t, broker, topic)

	return broker
}

func createKafkaTopic(t *testing.T, broker string, topic string) {
	conn, err := kafka_go.Dial("tcp", broker)
	require.NoError(t, err)
	defer conn.Close()

	err = conn.CreateTopics(kafka_go.TopicConfig{
		Topic:             topic,
		NumPartitions:     1,
		ReplicationFactor: 1,
	})
	require.NoError(t, err)
}

func seedPostgres(ctx context.Context, opts SeedOptions, pool *pgxpool.Pool, tableName string) {
	if opts.ID == uuid.Nil {
		opts.ID = uuid.New()
	}
	if opts.CreatedAt.IsZero() {
		opts.CreatedAt = time.Now().Add(-1 * time.Minute)
	}
	if opts.AvailableAt.IsZero() {
		opts.AvailableAt = opts.CreatedAt
	}
	if opts.Status == "" {
		opts.Status = relay.EventStatusPending
	}
	if opts.Status == relay.EventStatusDelivered && opts.DeliveredAt == nil {
		opts.DeliveredAt = &opts.CreatedAt
	}
	if opts.UpdatedAt == nil {
		opts.UpdatedAt = &opts.CreatedAt
	}

	query := fmt.Sprintf(`
			INSERT INTO %s (
				event_id, event_type, payload, status,
				created_at, available_at, delivered_at, updated_at, locked_at,
				locked_by, attempts

			) VALUES ($1, 'test.event', '{}', $2, $3, $4, $5, $6, $7, $8, $9)`, tableName)

	_, err := pool.Exec(ctx, query,
		opts.ID, opts.Status,
		opts.CreatedAt, opts.AvailableAt, opts.DeliveredAt, opts.UpdatedAt, opts.LockedAt,
		opts.LockedBy, opts.Attempts,
	)
	if err != nil {
		panic(fmt.Sprintf("failed to seed postgres: %v", err))
	}
}
