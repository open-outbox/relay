//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestEngine_Integration_Postgres(t *testing.T) {
	ctx := context.Background()

	// 1. Spin up a real Postgres container
	pgContainer, err := postgres.Run(ctx,
		"postgres:15-alpine",
		postgres.WithDatabase("openoutbox_db"),
		postgres.WithUsername("user"),
		postgres.WithPassword("pass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer pgContainer.Terminate(ctx)

	// 2. Get the connection string
	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	assert.NoError(t, err)

	// 3. Setup your "Real" Storage
	// Assuming you have a PostgreStorage implementation
	// storage, _ := NewPostgresStorage(connStr)
	// storage.Migrate() // Run your DDL here

	t.Run("End-to-End: Claim and Publish", func(t *testing.T) {
		// Here you would:
		// 1. Insert a raw row into the DB manually
		// 2. Run e.process(ctx)
		// 3. Query the DB to see if the status changed to 'DELIVERED'
		assert.NotEmpty(t, connStr)
	})
}
