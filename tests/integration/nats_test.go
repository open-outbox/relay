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
	db, pgConnStr := setupTestPostgres(t)

	nc, natsUrl := setupNats(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	setNatsRelayEnv(t, pgConnStr, natsUrl)

	// Build Application via DI Container
	di, err := container.BuildContainer(ctx)
	require.NoError(t, err)

	// The server is shared, so the stream and subjects are this test's own.
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats_go.StreamConfig{
		Name:     "RELAY_TESTS_HAPPY",
		Subjects: []string{"relay-tests.happy.>"},
	})
	require.NoError(t, err, "Failed to create JetStream stream")
	t.Cleanup(func() { _ = js.DeleteStream("RELAY_TESTS_HAPPY") })

	topic := "relay-tests.happy.v1"
	sub, err := nc.SubscribeSync(topic)
	require.NoError(t, err)

	eventID := uuid.New()
	payload := []byte(`{"order_id": 123, "status": "paid"}`)

	_, err = db.Exec(`
		INSERT INTO openoutbox_events (event_id, event_type, payload, status)
		VALUES ($1, $2, $3, 'PENDING')`,
		eventID, topic, payload)
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
	}, 2*time.Second, 50*time.Millisecond)
}

func TestNats_PublisherErrorHandling(t *testing.T) {
	db, pgConnStr := setupTestPostgres(t)
	_, natsUrl := setupNats(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	setNatsRelayEnv(t, pgConnStr, natsUrl)

	di, _ := container.BuildContainer(ctx)

	// No stream covers this subject, so NATS rejects the publish.
	eventID := uuid.New()
	_, err := db.Exec(`INSERT INTO openoutbox_events (event_id, event_type, payload, status)
                       VALUES ($1, 'relay-tests.nostream.v1', '{}', 'PENDING')`, eventID)
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
	}, 5*time.Second, 50*time.Millisecond, "Engine should record failure when NATS reject publish")
}
