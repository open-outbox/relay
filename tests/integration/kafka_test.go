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
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")

	di, err := container.BuildContainer(ctx)
	require.NoError(t, err)

	err = di.Invoke(func(pub relay.Publisher) {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		assert.NoError(t, pub.Ping(pingCtx), "Publisher Ping method should be covered and pass")
	})
	require.NoError(t, err)

	err = di.Invoke(func(engine *relay.Engine) {
		go func() { _ = engine.Start(ctx) }()
	})
	require.NoError(t, err)

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
	_, err = db.Exec(
		`INSERT INTO openoutbox_events (event_id, event_type, payload) VALUES ($1, $2, $3)`,
		eventID,
		topic,
		payload,
	)
	require.NoError(t, err)

	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err)
	assert.Equal(t, payload, msg.Value)

	assert.Eventually(t, func() bool {
		var status string
		db.QueryRow("SELECT status FROM openoutbox_events WHERE event_id = $1", eventID).
			Scan(&status)
		return status == "DELIVERED"
	}, 5*time.Second, 200*time.Millisecond)
}

// setKafkaRelayEnv points the relay at the given Postgres and Kafka instances.
func setKafkaRelayEnv(t *testing.T, pgConnStr, kafkaBrokers string) {
	t.Helper()
	t.Setenv("STORAGE_TYPE", "postgres")
	t.Setenv("STORAGE_URL", pgConnStr)
	t.Setenv("PUBLISHER_TYPE", "kafka")
	t.Setenv("PUBLISHER_URL", kafkaBrokers)
	t.Setenv("POLL_INTERVAL", "100ms")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
}

// startKafkaEngine builds the DI container and starts the engine in the background.
func startKafkaEngine(ctx context.Context, t *testing.T) {
	t.Helper()
	di, err := container.BuildContainer(ctx)
	require.NoError(t, err)

	err = di.Invoke(func(engine *relay.Engine) {
		go func() { _ = engine.Start(ctx) }()
	})
	require.NoError(t, err)
}

// An unknown topic is a non-retryable error: the event must be killed on the
// first attempt rather than scheduled for retry.
func TestKafka_UnknownTopic_MarksEventDead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, pgConnStr := setupTestPostgres(t)
	kafkaBrokers := setupKafka(t, "unused-topic")
	setKafkaRelayEnv(t, pgConnStr, kafkaBrokers)

	eventID := uuid.New()
	_, err := db.Exec(`
        INSERT INTO openoutbox_events (event_id, event_type, payload)
        VALUES ($1, $2, '{"data":"fail"}')`,
		eventID, "this.topic.does.not.exist")
	require.NoError(t, err)

	startKafkaEngine(ctx, t)

	var (
		status    string
		attempts  int
		lastError *string
	)
	require.Eventually(t, func() bool {
		err := db.QueryRow(
			"SELECT status, attempts, last_error FROM openoutbox_events WHERE event_id = $1",
			eventID,
		).Scan(&status, &attempts, &lastError)
		return err == nil && status == "DEAD"
	}, 10*time.Second, 200*time.Millisecond, "event with unknown topic should be marked DEAD")

	assert.Equal(t, 1, attempts, "non-retryable errors must not be retried")
	require.NotNil(t, lastError)
	assert.Contains(t, *lastError, "Unknown Topic Or Partition")
}

// A broker outage is a retryable error: the event must go back to PENDING
// with an incremented attempt count and an available_at in the future.
func TestKafka_BrokerUnavailable_SchedulesRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	topic := "outbox.retry.v1"
	db, pgConnStr := setupTestPostgres(t)
	kafkaBrokers, kafkaContainer := setupKafkaContainer(t, topic)
	setKafkaRelayEnv(t, pgConnStr, kafkaBrokers)

	// Fail fast once the broker is gone, and push the retry far enough out
	// that the engine won't pick the event up again during the test.
	t.Setenv("KAFKA_MAX_ATTEMPTS", "1")
	t.Setenv("KAFKA_CONNECTION_TIMEOUT", "1s")
	t.Setenv("KAFKA_WRITE_TIMEOUT", "2s")
	t.Setenv("KAFKA_READ_TIMEOUT", "2s")
	t.Setenv("RETRY_BASE_DELAY", "1m")

	startKafkaEngine(ctx, t)

	// Deliver a warm-up event first, so we know the engine is connected
	// before the broker goes away.
	warmupID := uuid.New()
	_, err := db.Exec(
		`INSERT INTO openoutbox_events (event_id, event_type, payload) VALUES ($1, $2, '{}')`,
		warmupID,
		topic,
	)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		var status string
		err := db.QueryRow("SELECT status FROM openoutbox_events WHERE event_id = $1", warmupID).
			Scan(&status)
		return err == nil && status == "DELIVERED"
	}, 20*time.Second, 200*time.Millisecond, "warm-up event should be delivered")

	stopTimeout := 5 * time.Second
	require.NoError(t, kafkaContainer.Stop(ctx, &stopTimeout))

	eventID := uuid.New()
	insertedAt := time.Now()
	_, err = db.Exec(
		`INSERT INTO openoutbox_events (event_id, event_type, payload) VALUES ($1, $2, '{}')`,
		eventID,
		topic,
	)
	require.NoError(t, err)

	var (
		status      string
		attempts    int
		availableAt time.Time
		lastError   *string
	)
	require.Eventually(t, func() bool {
		err := db.QueryRow(
			"SELECT status, attempts, available_at, last_error FROM openoutbox_events WHERE event_id = $1",
			eventID,
		).Scan(&status, &attempts, &availableAt, &lastError)
		return err == nil && attempts > 0 && status != "DELIVERING"
	}, 30*time.Second, 200*time.Millisecond, "engine should record the failed publish")

	assert.Equal(t, "PENDING", status, "retryable failures must not kill the event")
	assert.Equal(t, 1, attempts)
	assert.True(t, availableAt.After(insertedAt.Add(30*time.Second)),
		"available_at should be pushed out by the retry backoff, got %s", availableAt)
	require.NotNil(t, lastError)
	assert.Contains(t, *lastError, "kafka")
}
