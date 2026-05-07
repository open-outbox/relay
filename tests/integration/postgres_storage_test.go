//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresStorage(t *testing.T) {
	ctx := context.Background()
	tableName := "openoutbox_events"
	relayID := "storage1"
	relayID2 := "storage2"

	// Start Docker Postgres & get connection pool
	// Assuming setupTestPostgres returns (*sql.DB, string)
	// We use the connection string to create the pgxpool
	_, connStr := setupTestPostgres(t)
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	defer pool.Close()

	tel, err := relay.CreateNoopTelemetry()
	assert.NoError(t, err)

	// Initialize the Storage implementation
	store, err := storage.NewPostgres(pool, tableName, relayID, tel)
	require.NoError(t, err)
	defer store.Close(ctx)

	store2, err := storage.NewPostgres(pool, tableName, relayID2, tel)
	require.NoError(t, err)
	defer store2.Close(ctx)

	// Ping the connection
	err = store.Ping(ctx)
	assert.NoError(t, err)
	err = store2.Ping(ctx)
	assert.NoError(t, err)

	// Truncate
	truncate := func() {
		pool.Exec(context.Background(), "TRUNCATE TABLE openoutbox_events")
	}

	seeder := &PostgresSeeder{t: t, ctx: ctx, pool: pool, tableName: "openoutbox_events"}

	// Run the contract battery
	runStorageContractTest(t, store, store2, seeder, truncate)
}
