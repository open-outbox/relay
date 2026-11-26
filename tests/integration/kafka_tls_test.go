//go:build integration

package integration

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The relay's Kafka TLS settings are exercised against real brokers whose
// client listener only accepts TLS. Two brokers are shared across subtests:
// one with server-side TLS only, one that also requires client certificates.
func TestKafkaTLS(t *testing.T) {
	db, pgConnStr := setupTestPostgres(t)

	// Both brokers start at the same time, which saves one broker startup.
	brokers := setupKafkaTLSBrokers(t, false, true)
	oneWay, mutual := brokers[0], brokers[1]

	t.Run("OneWayTLS", func(t *testing.T) {
		broker, certs := oneWay.Addr, oneWay.Certs
		createKafkaTopic(t, certs.Dialer(), broker, "tls.ca.v1")
		createKafkaTopic(t, certs.Dialer(), broker, "tls.insecure.v1")

		t.Run("CA_VerifiesBrokerAndDelivers", func(t *testing.T) {
			setKafkaRelayEnv(t, pgConnStr, broker)
			t.Setenv("KAFKA_TLS_CA", "file://"+certs.CAFile)

			assertPublisherPing(t, nil)
			assertDelivered(t, db, broker, certs.Dialer(), "tls.ca.v1")
		})

		t.Run("Insecure_SkipsVerificationAndDelivers", func(t *testing.T) {
			setKafkaRelayEnv(t, pgConnStr, broker)
			t.Setenv("KAFKA_INSECURE", "true")

			assertPublisherPing(t, nil)
			assertDelivered(t, db, broker, certs.Dialer(), "tls.insecure.v1")
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
		// listener cannot accept. The TCP connect succeeds, so only Ping's
		// metadata round trip can tell; the engine must then never start
		// publishing, so no attempts are spent on a misconfiguration.
		t.Run("Plaintext_AgainstTLSListener_Fails", func(t *testing.T) {
			setKafkaRelayEnv(t, pgConnStr, broker)
			setFailFastKafkaEnv(t)

			assertPublisherPing(t, func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "metadata request failed")
			})
			assertEngineHoldsEvent(t, db, "tls.ca.v1")
		})
	})

	t.Run("MutualTLS", func(t *testing.T) {
		broker, certs := mutual.Addr, mutual.Certs
		createKafkaTopic(t, certs.Dialer(), broker, "mtls.v1")

		t.Run("ClientCert_Delivers", func(t *testing.T) {
			setKafkaRelayEnv(t, pgConnStr, broker)
			t.Setenv("KAFKA_TLS_CA", "file://"+certs.CAFile)
			t.Setenv("KAFKA_TLS_CERT", "file://"+certs.ClientCertFile)
			t.Setenv("KAFKA_TLS_KEY", "file://"+certs.ClientKeyFile)

			assertPublisherPing(t, nil)
			assertDelivered(t, db, broker, certs.Dialer(), "mtls.v1")
		})

		// The broker rejects the connection when no client certificate is
		// presented. With TLS 1.3 that rejection only surfaces on the first
		// read after the handshake, so a bare dial would pass; Ping's
		// metadata round trip must catch it before the engine publishes.
		t.Run("MissingClientCert_PingFails", func(t *testing.T) {
			setKafkaRelayEnv(t, pgConnStr, broker)
			setFailFastKafkaEnv(t)
			t.Setenv("KAFKA_TLS_CA", "file://"+certs.CAFile)

			assertPublisherPing(t, func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "metadata request failed")
				assert.Contains(t, err.Error(), "tls:")
			})
			assertEngineHoldsEvent(t, db, "mtls.v1")
		})
	})
}
