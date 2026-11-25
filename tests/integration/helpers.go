//go:build integration

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	dockercontainer "github.com/moby/moby/api/types/container"
	nats_go "github.com/nats-io/nats.go"
	"github.com/open-outbox/relay/internal/relay"
	redis_go "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/nats"
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

// setupTestPostgres returns a fresh database with the outbox schema. All tests
// share one Postgres container; each gets its own database, cloned from the
// schema template, which is dropped when the test ends.
func setupTestPostgres(t *testing.T) (*sql.DB, string) {
	t.Helper()
	ctx := context.Background()

	pg := sharedPostgres.get(t)
	name := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")

	_, err := pg.admin.ExecContext(ctx, fmt.Sprintf(
		"CREATE DATABASE %s TEMPLATE %s", name, postgresTemplateDB,
	))
	require.NoError(t, err)

	connStr := pg.connString(name)
	db, err := sql.Open("pgx", connStr)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = db.Close()
		// FORCE closes connections the engine under test may still hold.
		_, _ = pg.admin.ExecContext(
			context.Background(),
			fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name),
		)
	})

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
	client, url, _ := setupRedisContainer(t)
	return client, url
}

// setupRedisContainer is like setupRedis but also returns the container, so
// tests can stop Redis to simulate an outage. The container is removed when
// the test ends.
func setupRedisContainer(t *testing.T) (*redis_go.Client, string, *redis.RedisContainer) {
	t.Helper()
	ctx := context.Background()

	redisContainer, err := redis.Run(ctx, redisImage)
	t.Cleanup(func() {
		if redisContainer != nil {
			_ = redisContainer.Terminate(context.Background())
		}
	})
	require.NoError(t, err)

	endpoint, err := redisContainer.Endpoint(ctx, "")
	require.NoError(t, err)
	redisURL := "redis://" + endpoint

	opts, err := redis_go.ParseURL(redisURL)
	require.NoError(t, err)
	client := redis_go.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.Ping(ctx).Err())

	return client, redisURL, redisContainer
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
