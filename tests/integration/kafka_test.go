//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/container"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKafkaHappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	topic := "outbox.events.v1"

	db, pgConnStr := setupTestPostgres(t)
	kafkaBrokers := setupKafka(t, topic)

	t.Setenv("STORAGE_TYPE", "postgres")
	t.Setenv("STORAGE_URL", pgConnStr)
	t.Setenv("PUBLISHER_TYPE", "kafka")
	t.Setenv("PUBLISHER_URL", kafkaBrokers)
	t.Setenv("POLL_INTERVAL", "100ms")

	di, _ := container.BuildContainer(ctx)
	di.Invoke(func(engine *relay.Engine) {
		go engine.Start(ctx)
	})

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: []string{kafkaBrokers},
		Topic:   topic,
		GroupID: "integration-test-group",
		Dialer: &kafka.Dialer{
			Timeout: 10 * time.Second,
		},
	})
	defer reader.Close()

	eventID := uuid.New()
	payload := []byte(`{"type":"kafka-test"}`)
	_, err := db.Exec(
		`INSERT INTO openoutbox_events (event_id, event_type, payload) VALUES ($1, $2, $3)`,
		eventID,
		topic,
		payload,
	)
	require.NoError(t, err)

	msg, err := reader.ReadMessage(ctx)
	assert.NoError(t, err)
	assert.Equal(t, payload, msg.Value)

	assert.Eventually(t, func() bool {
		var status string
		db.QueryRow("SELECT status FROM openoutbox_events WHERE event_id = $1", eventID).
			Scan(&status)
		return status == "DELIVERED"
	}, 5*time.Second, 200*time.Millisecond)
}

func TestKafka_PublishFailure_IncrementsAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, pgConnStr := setupTestPostgres(t)

	kafkaBrokers := setupKafka(t, "unused-topic")

	t.Setenv("STORAGE_TYPE", "postgres")
	t.Setenv("STORAGE_URL", pgConnStr)
	t.Setenv("PUBLISHER_TYPE", "kafka")
	t.Setenv("PUBLISHER_URL", kafkaBrokers)
	t.Setenv("POLL_INTERVAL", "100ms")

	// Force a failure: Set a tiny timeout so the publish fails
	t.Setenv("KAFKA_WRITE_TIMEOUT", "1ms")

	di, _ := container.BuildContainer(ctx)

	eventID := uuid.New()
	badTopic := "this.topic.does.not.exist"
	_, err := db.Exec(`
        INSERT INTO openoutbox_events (event_id, event_type, payload)
        VALUES ($1, $2, '{"data":"fail"}')`,
		eventID, badTopic)
	require.NoError(t, err)

	di.Invoke(func(engine *relay.Engine) {
		go engine.Start(ctx)
	})

	assert.Eventually(t, func() bool {
		var attempts int
		var status string
		err := db.QueryRow("SELECT attempts, status FROM openoutbox_events WHERE event_id = $1", eventID).
			Scan(&attempts, &status)

		return err == nil && attempts > 0 && status != "DELIVERED"
	}, 10*time.Second, 500*time.Millisecond, "Engine should have attempted to publish and failed")
}
