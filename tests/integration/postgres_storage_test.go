//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/storage"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestPostgresStorage(t *testing.T) {
	ctx := context.Background()
	tableName := "openoutbox_events"
	logger := zap.NewNop()

	// Start Docker Postgres & get connection pool
	// Assuming setupTestPostgres returns (*sql.DB, string)
	// We use the connection string to create the pgxpool
	_, connStr := setupTestPostgres(t)
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	defer pool.Close()

	// Initialize the Storage implementation
	store, err := storage.NewPostgres(pool, tableName, logger)
	require.NoError(t, err)
	defer store.Close(ctx)

	seed := func(opts seedOptions) {
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
			opts.Status = relay.StatusPending
		}
		if opts.Status == relay.StatusDelivered && opts.DeliveredAt == nil {
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
		require.NoError(t, err, "failed to seed event")
	}

	// Truncate
	truncate := func() {
		pool.Exec(context.Background(), "TRUNCATE TABLE openoutbox_events")
	}

	// Run the contract battery
	runStorageContractTest(t, store, seed, truncate)
}
