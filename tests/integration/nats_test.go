//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib" // Required for sql.Open with pgx
	nats_go "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-outbox/relay/internal/container"
	"github.com/open-outbox/relay/internal/relay"
)

func TestNatsHappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	db, pgConnStr := setupTestPostgres(t)

	nc, natsUrl := setupNats(t)

	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("STORAGE_TYPE", "postgres")
	t.Setenv("STORAGE_URL", pgConnStr)
	t.Setenv("PUBLISHER_TYPE", "nats")
	t.Setenv("PUBLISHER_URL", natsUrl)
	t.Setenv("POLL_INTERVAL", "100ms")
	t.Setenv("BATCH_SIZE", "10")

	// Build Application via DI Container
	di, err := container.BuildContainer(ctx)
	require.NoError(t, err)

	js, err := nc.JetStream()
	_, err = js.AddStream(&nats_go.StreamConfig{
		Name:     "OPENOUTBOX_EVENTS",
		Subjects: []string{"openoutbox.events.>"},
	})
	require.NoError(t, err, "Failed to create JetStream stream")

	// Setup Subscriber and Seed Data
	// Using the topic name from your NATS implementation
	topic := "openoutbox.events.v1"
	sub, err := nc.SubscribeSync(topic)
	require.NoError(t, err)

	eventID := uuid.New()
	payload := []byte(`{"order_id": 123, "status": "paid"}`)

	_, err = db.Exec(`
		INSERT INTO openoutbox_events (event_id, event_type, payload, status)
		VALUES ($1, $2, $3, 'PENDING')`,
		eventID, "openoutbox.events.v1", payload)
	require.NoError(t, err)

	err = di.Invoke(func(pub relay.Publisher) {
		pub.Connect(ctx)
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		assert.NoError(t, pub.Ping(pingCtx), "Publisher Ping method should be covered and pass")
	})
	require.NoError(t, err)

	// Start the Engine through DI
	err = di.Invoke(func(engine *relay.Engine) {
		go func() {
			if err := engine.Start(ctx); err != nil {
				// We expect context canceled error on cleanup
				t.Logf("Engine stopped: %v", err)
			}
		}()
	})
	require.NoError(t, err)

	// Assertions
	// Check NATS received the message
	msg, err := sub.NextMsg(5 * time.Second)
	assert.NoError(t, err, "Should have received message on NATS")
	require.NotNil(t, msg, "Message should not be nil")
	assert.Equal(t, payload, msg.Data)

	// Check DB updated to 'DELIVERED' (matching your schema status)
	var status string
	// Wait a moment for the DB write to propagate after NATS publish
	assert.Eventually(t, func() bool {
		err := db.QueryRow("SELECT status FROM openoutbox_events WHERE event_id = $1", eventID).
			Scan(&status)
		return err == nil && status == "DELIVERED"
	}, 2*time.Second, 100*time.Millisecond)
}

func TestNats_PublisherErrorHandling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, pgConnStr := setupTestPostgres(t)
	_, natsUrl := setupNats(t) // NATS is up, but we WON'T create a stream

	t.Setenv("STORAGE_TYPE", "postgres")
	t.Setenv("STORAGE_URL", pgConnStr)
	t.Setenv("PUBLISHER_TYPE", "nats")
	t.Setenv("PUBLISHER_URL", natsUrl)
	t.Setenv("POLL_INTERVAL", "100ms")

	di, _ := container.BuildContainer(ctx)

	// Insert an event.
	// Since we didn't call js.AddStream(), NATS will reject the publish.
	eventID := uuid.New()
	_, err := db.Exec(`INSERT INTO openoutbox_events (event_id, event_type, payload, status)
                       VALUES ($1, 'openoutbox.events.v1', '{}', 'PENDING')`, eventID)
	require.NoError(t, err)

	// Start the Engine through DI
	err = di.Invoke(func(engine *relay.Engine) {
		go func() {
			if err := engine.Start(ctx); err != nil {
				// We expect context canceled error on cleanup
				t.Logf("Engine stopped: %v", err)
			}
		}()
	})
	require.NoError(t, err)

	// ASSERTION: The engine should increment 'attempts' because NATS returned an error
	assert.Eventually(t, func() bool {
		var attempts int
		var status string
		err := db.QueryRow("SELECT attempts, status FROM openoutbox_events WHERE event_id = $1", eventID).
			Scan(&attempts, &status)

		// We want to see attempts > 0 and status still PENDING (or DELIVERING)
		return err == nil && attempts > 0 && status != "DELIVERED"
	}, 5*time.Second, 200*time.Millisecond, "Engine should record failure when NATS reject publish")
}
