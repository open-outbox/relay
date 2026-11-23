//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/container"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The relay's Kafka TLS settings are exercised against real brokers whose
// client listener only accepts TLS. Two brokers are shared across subtests:
// one with server-side TLS only, one that also requires client certificates.
func TestKafkaTLS(t *testing.T) {
	db, pgConnStr := setupTestPostgres(t)

	t.Run("OneWayTLS", func(t *testing.T) {
		broker, certs := setupKafkaTLS(t, "tls.ca.v1", false)
		createKafkaTopicWithDialer(t, certs.Dialer(), broker, "tls.insecure.v1")

		t.Run("CA_VerifiesBrokerAndDelivers", func(t *testing.T) {
			setKafkaRelayEnv(t, pgConnStr, broker)
			t.Setenv("KAFKA_TLS_CA", "file://"+certs.CAFile)

			assertPublisherPing(t, nil)
			assertDeliveredOverTLS(t, db, broker, certs, "tls.ca.v1")
		})

		t.Run("Insecure_SkipsVerificationAndDelivers", func(t *testing.T) {
			setKafkaRelayEnv(t, pgConnStr, broker)
			t.Setenv("KAFKA_INSECURE", "true")

			assertPublisherPing(t, nil)
			assertDeliveredOverTLS(t, db, broker, certs, "tls.insecure.v1")
		})

		// KAFKA_TLS_ENABLED with no CA verifies against the system trust
		// store; our private test CA is not in it, so the handshake must be
		// rejected rather than silently skipping verification.
		t.Run("TLSEnabled_UsesSystemRoots_RejectsPrivateCA", func(t *testing.T) {
			setKafkaRelayEnv(t, pgConnStr, broker)
			t.Setenv("KAFKA_TLS_ENABLED", "true")

			assertPublisherPing(t, func(t *testing.T, err error) {
				// The message differs per platform verifier (Linux: "signed by
				// unknown authority", macOS: "certificate is not trusted"), so
				// match on the error type instead.
				var verifyErr *tls.CertificateVerificationError
				require.ErrorAs(
					t,
					err,
					&verifyErr,
					"expected a certificate verification failure, got: %v",
					err,
				)
			})
		})

		// Without any TLS setting the relay speaks plaintext, which a TLS
		// listener cannot accept.
		t.Run("Plaintext_AgainstTLSListener_Fails", func(t *testing.T) {
			setKafkaRelayEnv(t, pgConnStr, broker)
			setFailFastKafkaEnv(t)

			eventID := insertKafkaEvent(t, db, "tls.ca.v1")
			ctx := startKafkaEngineFor(t, 20*time.Second)
			assertPublishFailedAndRetried(ctx, t, db, eventID)
		})
	})

	t.Run("MutualTLS", func(t *testing.T) {
		broker, certs := setupKafkaTLS(t, "mtls.v1", true)

		t.Run("ClientCert_Delivers", func(t *testing.T) {
			setKafkaRelayEnv(t, pgConnStr, broker)
			t.Setenv("KAFKA_TLS_CA", "file://"+certs.CAFile)
			t.Setenv("KAFKA_TLS_CERT", "file://"+certs.ClientCertFile)
			t.Setenv("KAFKA_TLS_KEY", "file://"+certs.ClientKeyFile)

			assertPublisherPing(t, nil)
			assertDeliveredOverTLS(t, db, broker, certs, "mtls.v1")
		})

		// The broker rejects the connection when no client certificate is
		// presented. With TLS 1.3 that rejection only surfaces after the
		// handshake, so it is asserted through a real publish attempt: the
		// event must stay undelivered and be scheduled for retry.
		t.Run("MissingClientCert_PublishFails", func(t *testing.T) {
			setKafkaRelayEnv(t, pgConnStr, broker)
			setFailFastKafkaEnv(t)
			t.Setenv("KAFKA_TLS_CA", "file://"+certs.CAFile)

			eventID := insertKafkaEvent(t, db, "mtls.v1")
			ctx := startKafkaEngineFor(t, 30*time.Second)
			assertPublishFailedAndRetried(ctx, t, db, eventID)
		})
	})
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
			assert.NoError(t, err, "publisher should reach the TLS broker")
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

// assertDeliveredOverTLS runs the engine, inserts an event and checks that
// it is consumed from the TLS broker and marked DELIVERED.
func assertDeliveredOverTLS(
	t *testing.T,
	db *sql.DB,
	broker string,
	certs *kafkaTLSCerts,
	topic string,
) {
	t.Helper()
	ctx := startKafkaEngineFor(t, 60*time.Second)

	eventID := insertKafkaEvent(t, db, topic)

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     []string{broker},
		Topic:       topic,
		Partition:   0,
		StartOffset: kafka.FirstOffset,
		Dialer:      certs.Dialer(),
	})
	defer reader.Close()

	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err, "message should be readable from the TLS broker")
	assert.JSONEq(t, `{"id":"`+eventID.String()+`"}`, string(msg.Value))

	assert.Eventually(t, func() bool {
		var status string
		err := db.QueryRow("SELECT status FROM openoutbox_events WHERE event_id = $1", eventID).
			Scan(&status)
		return err == nil && status == "DELIVERED"
	}, 10*time.Second, 200*time.Millisecond, "event should be marked DELIVERED")
}

// assertPublishFailedAndRetried expects the event's publish to fail with a
// retryable error: it goes back to PENDING with one recorded attempt.
func assertPublishFailedAndRetried(
	ctx context.Context,
	t *testing.T,
	db *sql.DB,
	eventID uuid.UUID,
) {
	t.Helper()
	var (
		status    string
		attempts  int
		lastError *string
	)
	require.Eventually(t, func() bool {
		if ctx.Err() != nil {
			return false
		}
		err := db.QueryRow(
			"SELECT status, attempts, last_error FROM openoutbox_events WHERE event_id = $1",
			eventID,
		).Scan(&status, &attempts, &lastError)
		return err == nil && attempts > 0 && status != "DELIVERING"
	}, 25*time.Second, 200*time.Millisecond, "engine should record the failed publish")

	assert.Equal(t, "PENDING", status, "TLS failures are retryable and must not kill the event")
	require.NotNil(t, lastError)
	t.Logf("recorded publish error: %s", *lastError)
}
