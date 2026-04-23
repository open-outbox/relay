//go:build integrationx

package integration

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-outbox/relay/internal/storage"
	"github.com/stretchr/testify/assert"
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

	// Ping the connection
	err = store.Ping(ctx)
	assert.NoError(t, err)

	// Truncate
	truncate := func() {
		pool.Exec(context.Background(), "TRUNCATE TABLE openoutbox_events")
	}

	seeder := &PostgresSeeder{t: t, ctx: ctx, pool: pool, tableName: "openoutbox_events"}

	// Run the contract battery
	runStorageContractTest(t, store, seeder, truncate)
}
