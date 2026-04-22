//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/container"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/redis"
)

func TestRedis_Publish_HappyPath(t *testing.T) {
	// Use a slightly longer timeout for the whole test to account for container startup
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Setup Infrastructure
	db, pgConnStr := setupTestPostgres(t)
	r, redisUrl := setupRedis(t)

	// Configure Environment for the DI Container
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("STORAGE_TYPE", "postgres")
	t.Setenv("STORAGE_URL", pgConnStr)
	t.Setenv("PUBLISHER_TYPE", "redis")
	t.Setenv("PUBLISHER_URL", redisUrl)
	t.Setenv("POLL_INTERVAL", "100ms")
	t.Setenv("BATCH_SIZE", "10")
	t.Setenv("LEASE_TIMEOUT", "5s") // Small lease for fast testing

	// Seed the Database
	eventID := uuid.New()
	payload := []byte(`{"order_id": 123, "status": "paid"}`)
	eventType := "openoutbox.events.v1"

	_, err := db.ExecContext(ctx, `
        INSERT INTO openoutbox_events (event_id, event_type, payload, status, available_at)
        VALUES ($1, $2, $3, 'PENDING', NOW())`,
		eventID, eventType, payload)
	require.NoError(t, err)

	// Build Application via DI Container
	di, err := container.BuildContainer(ctx)
	require.NoError(t, err)

	// Start the Engine
	// We create a specific context for the engine so we can shut it down
	engineCtx, engineCancel := context.WithCancel(ctx)
	defer engineCancel()

	err = di.Invoke(func(engine *relay.Engine) {
		go func() {
			if err := engine.Start(engineCtx); err != nil {
				t.Logf("Engine stopped: %v", err)
			}
		}()
	})
	require.NoError(t, err)

	// Assert: Verify the data reached Redis
	// We use Eventually because the engine polls every 100ms
	require.Eventually(t, func() bool {
		res, err := r.XRange(ctx, eventType, "-", "+").Result()
		return err == nil && len(res) == 1
	}, 10*time.Second, 200*time.Millisecond, "Event should be published to Redis stream")

	// Detailed Data Validation
	res, err := r.XRange(ctx, eventType, "-", "+").Result()
	require.NoError(t, err)

	values := res[0].Values
	assert.Equal(t, eventID.String(), values["id"])
	assert.Equal(t, string(payload), values["payload"])

	// Optional: Verify DB status changed to COMPLETED (or was deleted by reaper)
	var status string
	err = db.QueryRowContext(ctx, "SELECT status FROM openoutbox_events WHERE event_id = $1", eventID).
		Scan(&status)
	if err == nil {
		assert.Equal(t, "DELIVERED", status)
	}
}

func TestRedis_PublisherErrorHandling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	db, pgConnStr := setupTestPostgres(t)
	// Setup Redis but keep the container handle so we can stop it
	ctxBG := context.Background()
	redisContainer, err := redis.Run(ctxBG, "redis:7-alpine")
	require.NoError(t, err)
	endpoint, _ := redisContainer.Endpoint(ctxBG, "")
	redisUrl := "redis://" + endpoint

	t.Setenv("STORAGE_TYPE", "postgres")
	t.Setenv("STORAGE_URL", pgConnStr)
	t.Setenv("PUBLISHER_TYPE", "redis")
	t.Setenv("PUBLISHER_URL", redisUrl)
	t.Setenv("POLL_INTERVAL", "100ms")
	t.Setenv("LEASE_TIMEOUT", "1m")
	t.Setenv("REDIS_WRITE_TIMEOUT", "100ms")

	// Build and Start Engine
	di, err := container.BuildContainer(ctx)
	require.NoError(t, err)

	err = di.Invoke(func(pub relay.Publisher) {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		assert.NoError(t, pub.Ping(pingCtx), "Publisher Ping method should be covered and pass")
	})
	require.NoError(t, err)

	err = di.Invoke(func(engine *relay.Engine) {
		go func() {
			_ = engine.Start(ctx)
		}()
	})
	require.NoError(t, err)

	// KILL REDIS immediately after starting the engine
	// This ensures the first publish attempt fails.
	err = redisContainer.Terminate(ctxBG)
	require.NoError(t, err)

	// Insert an event
	eventID := uuid.New()
	_, err = db.Exec(`INSERT INTO openoutbox_events (event_id, event_type, payload, status)
                       VALUES ($1, 'sad.path.stream', '{}', 'PENDING')`, eventID)
	require.NoError(t, err)

	// ASSERTION: The engine should increment 'attempts'
	// because the Redis client will return a connection error.
	assert.Eventually(t, func() bool {
		var attempts int
		// Use db.QueryRowContext to ensure we aren't using a stale connection
		err := db.QueryRowContext(ctx, "SELECT attempts FROM openoutbox_events WHERE event_id = $1", eventID).
			Scan(&attempts)

		// We just want to see that the engine managed to write back 'attempts = 1'
		return err == nil && attempts > 0
	}, 5*time.Second, 200*time.Millisecond, "Engine should record failure when Redis is unreachable")

}
