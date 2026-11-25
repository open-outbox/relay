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
)

func TestRedis_Publish_HappyPath(t *testing.T) {
	// Setup Infrastructure
	db, pgConnStr := setupTestPostgres(t)
	r, redisUrl := setupRedis(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Configure Environment for the DI Container
	setRedisRelayEnv(t, pgConnStr, redisUrl)

	// Seed the Database
	eventID := uuid.New()
	payload := []byte(`{"order_id": 123, "status": "paid"}`)
	eventType := "openoutbox.events.v1"

	_, err := db.ExecContext(ctx, `
        INSERT INTO openoutbox_events (
            event_id, event_type, payload, partition_key, headers, status, available_at
        )
        VALUES ($1, $2, $3, 'order-123', '{"trace_id":"abc"}', 'PENDING', NOW())`,
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
	}, 10*time.Second, 50*time.Millisecond, "Event should be published to Redis stream")

	// Detailed Data Validation
	res, err := r.XRange(ctx, eventType, "-", "+").Result()
	require.NoError(t, err)

	values := res[0].Values
	assert.Equal(t, eventID.String(), values["id"])
	assert.Equal(t, string(payload), values["payload"])
	assert.Equal(t, "order-123", values["partition_key"])
	assert.Equal(t, "abc", values["h:trace_id"], "headers are stored with an h: prefix")

	// Optional: Verify DB status changed to COMPLETED (or was deleted by reaper)
	var status string
	err = db.QueryRowContext(ctx, "SELECT status FROM openoutbox_events WHERE event_id = $1", eventID).
		Scan(&status)
	if err == nil {
		assert.Equal(t, "DELIVERED", status)
	}
}

func TestRedis_PublisherErrorHandling(t *testing.T) {
	db, pgConnStr := setupTestPostgres(t)
	_, redisUrl, redisContainer := setupRedisContainer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	setRedisRelayEnv(t, pgConnStr, redisUrl)
	t.Setenv("REDIS_WRITE_TIMEOUT", "100ms")
	// Keep the first health check out of the test's window, so only the
	// failed publish is under test.
	t.Setenv("HEALTH_CHECK_INTERVAL", "1m")

	// Build and Start Engine
	di, err := container.BuildContainer(ctx)
	require.NoError(t, err)

	err = di.Invoke(func(pub relay.Publisher) {
		pub.Connect(ctx)
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

	// Deliver a warm-up event first, so we know the engine is connected
	// and processing before Redis goes away.
	warmupID := uuid.New()
	_, err = db.Exec(`INSERT INTO openoutbox_events (event_id, event_type, payload, status)
                       VALUES ($1, 'warmup.stream', '{}', 'PENDING')`, warmupID)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		var status string
		err := db.QueryRowContext(ctx, "SELECT status FROM openoutbox_events WHERE event_id = $1", warmupID).
			Scan(&status)
		return err == nil && status == "DELIVERED"
	}, 10*time.Second, 50*time.Millisecond, "warm-up event should be delivered")

	// Kill Redis at once; setupRedisContainer removes it when the test ends.
	noWait := time.Duration(0)
	require.NoError(t, redisContainer.Stop(ctx, &noWait))

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
	}, 5*time.Second, 50*time.Millisecond, "Engine should record failure when Redis is unreachable")

}

// A stream name that already holds a plain key can never be written with
// XADD (WRONGTYPE), so the event must be killed on the first attempt.
func TestRedis_WrongTypeKey_MarksEventDead(t *testing.T) {
	db, pgConnStr := setupTestPostgres(t)
	r, redisUrl := setupRedis(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	setRedisRelayEnv(t, pgConnStr, redisUrl)

	const key = "orders.not.a.stream"
	require.NoError(t, r.Set(ctx, key, "plain value", 0).Err())

	eventID := uuid.New()
	_, err := db.ExecContext(ctx, `
        INSERT INTO openoutbox_events (event_id, event_type, payload)
        VALUES ($1, $2, '{}')`,
		eventID, key)
	require.NoError(t, err)

	di, err := container.BuildContainer(ctx)
	require.NoError(t, err)
	require.NoError(t, di.Invoke(func(engine *relay.Engine) {
		go func() { _ = engine.Start(ctx) }()
	}))

	var (
		status    string
		attempts  int
		lastError *string
	)
	require.Eventually(t, func() bool {
		err := db.QueryRowContext(ctx,
			"SELECT status, attempts, last_error FROM openoutbox_events WHERE event_id = $1",
			eventID,
		).Scan(&status, &attempts, &lastError)
		return err == nil && status == "DEAD"
	}, 10*time.Second, 50*time.Millisecond, "event for a non-stream key should be marked DEAD")

	assert.Equal(t, 1, attempts, "non-retryable errors must not be retried")
	require.NotNil(t, lastError)
	assert.Contains(t, *lastError, "WRONGTYPE")
}
