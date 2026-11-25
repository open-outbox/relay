//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/container"
	"github.com/open-outbox/relay/internal/relay"
	kafka_go "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/kafka"
)

// setupKafka returns the shared broker's address, with topic created on it.
func setupKafka(t *testing.T, topic string) string {
	t.Helper()
	broker := sharedKafka.get(t)
	createKafkaTopic(t, nil, broker, topic)
	return broker
}

// setupKafkaContainer starts a broker of the test's own, for tests that stop
// it to simulate an outage.
func setupKafkaContainer(t *testing.T, topic string) (string, *kafka.KafkaContainer) {
	t.Helper()
	ctx := context.Background()

	kafkaContainer, err := kafka.Run(ctx, kafkaImage)
	t.Cleanup(func() {
		if kafkaContainer != nil {
			_ = kafkaContainer.Terminate(ctx)
		}
	})
	require.NoError(t, err)

	brokers, err := kafkaContainer.Brokers(ctx)
	require.NoError(t, err)
	broker := brokers[0]

	createKafkaTopic(t, nil, broker, topic)
	return broker, kafkaContainer
}

// oversizedKafkaPayload returns a JSON payload larger than the broker's
// default 1MB message.max.bytes. Random bytes don't compress, so the record
// stays oversized even with snappy enabled. Publishing it fails with the
// non-retryable MessageSizeTooLarge error.
func oversizedKafkaPayload(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 3<<20)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	return fmt.Sprintf(`{"data":%q}`, base64.StdEncoding.EncodeToString(raw))
}

// setKafkaRelayEnv points the relay at the given Postgres and Kafka instances.
func setKafkaRelayEnv(t *testing.T, pgConnStr, kafkaBrokers string) {
	t.Helper()
	setRelayEnv(t, pgConnStr, "kafka", kafkaBrokers)
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

// setFailFastKafkaEnv makes a failing publish surface quickly, and pushes the
// retry far enough out that the event is not re-claimed during the test.
func setFailFastKafkaEnv(t *testing.T) {
	t.Helper()
	t.Setenv("KAFKA_MAX_ATTEMPTS", "1")
	t.Setenv("KAFKA_CONNECTION_TIMEOUT", "3s")
	t.Setenv("KAFKA_WRITE_TIMEOUT", "3s")
	t.Setenv("KAFKA_READ_TIMEOUT", "3s")
	t.Setenv("RETRY_BASE_DELAY", "1m")
}

// assertPublisherPing builds the relay from the current environment and
// pings the publisher. A nil check expects success.
func assertPublisherPing(t *testing.T, check func(t *testing.T, err error)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	di, err := container.BuildContainer(ctx)
	require.NoError(t, err)

	require.NoError(t, di.Invoke(func(pub relay.Publisher) {
		defer pub.Close(ctx)
		err := pub.Ping(ctx)
		if check == nil {
			assert.NoError(t, err, "publisher should reach the broker")
			return
		}
		check(t, err)
	}))
}

// startKafkaEngineFor starts the engine from the current environment. The
// engine is stopped when the returned context expires or the test ends.
func startKafkaEngineFor(t *testing.T, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	startKafkaEngine(ctx, t)
	return ctx
}

func insertKafkaEvent(t *testing.T, db *sql.DB, topic string) uuid.UUID {
	t.Helper()
	eventID := uuid.New()
	_, err := db.Exec(
		`INSERT INTO openoutbox_events (event_id, event_type, payload) VALUES ($1, $2, $3)`,
		eventID, topic, []byte(`{"id":"`+eventID.String()+`"}`),
	)
	require.NoError(t, err)
	return eventID
}

// assertDelivered runs the engine, inserts an event and checks that it is
// consumed from the broker and marked DELIVERED. The dialer is what the
// test's reader needs to reach the broker (TLS, SASL); nil uses plaintext.
func assertDelivered(
	t *testing.T,
	db *sql.DB,
	broker string,
	dialer *kafka_go.Dialer,
	topic string,
) {
	t.Helper()
	ctx := startKafkaEngineFor(t, 60*time.Second)

	eventID := insertKafkaEvent(t, db, topic)

	reader := kafka_go.NewReader(kafka_go.ReaderConfig{
		Brokers:     []string{broker},
		Topic:       topic,
		Partition:   0,
		StartOffset: kafka_go.FirstOffset,
		Dialer:      dialer,
	})
	defer reader.Close()

	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err, "message should be readable from the broker")
	assert.JSONEq(t, `{"id":"`+eventID.String()+`"}`, string(msg.Value))

	assert.Eventually(t, func() bool {
		var status string
		err := db.QueryRow("SELECT status FROM openoutbox_events WHERE event_id = $1", eventID).
			Scan(&status)
		return err == nil && status == "DELIVERED"
	}, 10*time.Second, 200*time.Millisecond, "event should be marked DELIVERED")
}

// assertEngineHoldsEvent starts the engine against a broker that Ping
// rejects and checks that the event is left untouched: still PENDING, with
// no attempt spent, because the engine never gets past connecting.
func assertEngineHoldsEvent(t *testing.T, db *sql.DB, topic string) {
	t.Helper()
	eventID := insertKafkaEvent(t, db, topic)
	ctx := startKafkaEngineFor(t, 8*time.Second)
	<-ctx.Done()

	var (
		status   string
		attempts int
	)
	require.NoError(t, db.QueryRow(
		"SELECT status, attempts FROM openoutbox_events WHERE event_id = $1", eventID,
	).Scan(&status, &attempts))
	assert.Equal(t, "PENDING", status, "event must not be claimed while the broker is unusable")
	assert.Equal(t, 0, attempts, "a misconfiguration must not spend delivery attempts")
}

// createKafkaTopic creates a single-partition topic. A nil dialer connects
// in plaintext. The topic may already exist, e.g. with -count=2.
func createKafkaTopic(t *testing.T, dialer *kafka_go.Dialer, broker, topic string) {
	t.Helper()
	if dialer == nil {
		dialer = kafka_go.DefaultDialer
	}
	conn, err := dialer.Dial("tcp", broker)
	require.NoError(t, err)
	defer conn.Close()

	err = conn.CreateTopics(kafka_go.TopicConfig{
		Topic:             topic,
		NumPartitions:     1,
		ReplicationFactor: 1,
	})
	if !errors.Is(err, kafka_go.TopicAlreadyExists) {
		require.NoError(t, err)
	}
}
