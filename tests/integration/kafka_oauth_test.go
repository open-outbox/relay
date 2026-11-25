//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	kafka_go "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/kafka"
)

// The broker's client listener accepts only SASL/OAUTHBEARER, validated by
// Kafka's built-in unsecured validator, which accepts unsigned JWTs. The
// relay gets its tokens from a local OAuth token endpoint.
func TestKafkaOAuthBearer(t *testing.T) {
	db, pgConnStr := setupTestPostgres(t)
	tokens := newUnsecuredTokenServer(t)
	broker := setupKafkaOAuth(t, "oauth.v1")

	setOAuthRelayEnv := func(t *testing.T) {
		setKafkaRelayEnv(t, pgConnStr, broker)
		t.Setenv("KAFKA_SASL_MECHANISM", "oauthbearer")
		t.Setenv("KAFKA_OAUTH_TOKEN_URL", tokens.URL)
		t.Setenv("KAFKA_OAUTH_CLIENT_ID", "relay")
		t.Setenv("KAFKA_OAUTH_CLIENT_SECRET", "secret")
	}

	t.Run("ValidToken_Delivers", func(t *testing.T) {
		setOAuthRelayEnv(t)

		assertPublisherPing(t, nil)

		ctx := startKafkaEngineFor(t, 60*time.Second)
		eventID := insertKafkaEvent(t, db, "oauth.v1")

		reader := kafka_go.NewReader(kafka_go.ReaderConfig{
			Brokers:     []string{broker},
			Topic:       "oauth.v1",
			Partition:   0,
			StartOffset: kafka_go.FirstOffset,
			Dialer:      oauthTestDialer(t),
		})
		defer reader.Close()

		msg, err := reader.ReadMessage(ctx)
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"`+eventID.String()+`"}`, string(msg.Value))

		assert.Eventually(t, func() bool {
			var status string
			err := db.QueryRow(
				"SELECT status FROM openoutbox_events WHERE event_id = $1", eventID,
			).Scan(&status)
			return err == nil && status == "DELIVERED"
		}, 10*time.Second, 200*time.Millisecond, "event should be marked DELIVERED")
	})

	// An expired token is rejected by the broker, so Ping fails and the
	// engine never claims the event.
	t.Run("ExpiredToken_PingFails", func(t *testing.T) {
		setOAuthRelayEnv(t)
		setFailFastKafkaEnv(t)
		tokens.expired.Store(true)
		t.Cleanup(func() { tokens.expired.Store(false) })

		assertPublisherPing(t, func(t *testing.T, err error) {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "broker rejected the token")
		})
		assertEngineHoldsEvent(t, db, "oauth.v1")
	})
}

type unsecuredTokenServer struct {
	*httptest.Server
	expired atomic.Bool
}

func newUnsecuredTokenServer(t *testing.T) *unsecuredTokenServer {
	t.Helper()
	s := &unsecuredTokenServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		lifetime := time.Hour
		if s.expired.Load() {
			lifetime = -time.Hour
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": unsecuredJWT(t, lifetime),
			"token_type":   "Bearer",
			// Short enough that the relay does not keep a token across subtests.
			"expires_in": 1,
		})
	}))
	t.Cleanup(s.Close)
	return s
}

func unsecuredJWT(t *testing.T, lifetime time.Duration) string {
	t.Helper()
	now := time.Now()
	encode := func(v any) string {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return encode(map[string]any{"alg": "none"}) + "." + encode(map[string]any{
		"sub": "relay",
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(lifetime).Unix(),
	}) + "."
}

type staticOAuthBearer struct{ token string }

func (m staticOAuthBearer) Name() string { return "OAUTHBEARER" }

func (m staticOAuthBearer) Start(context.Context) (sasl.StateMachine, []byte, error) {
	return m, []byte(fmt.Sprintf("n,,\x01auth=Bearer %s\x01\x01", m.token)), nil
}

func (m staticOAuthBearer) Next(_ context.Context, challenge []byte) (bool, []byte, error) {
	if len(challenge) > 0 {
		return true, nil, fmt.Errorf("token rejected: %s", challenge)
	}
	return true, nil, nil
}

func oauthTestDialer(t *testing.T) *kafka_go.Dialer {
	return &kafka_go.Dialer{
		Timeout:       10 * time.Second,
		DualStack:     true,
		SASLMechanism: staticOAuthBearer{token: unsecuredJWT(t, time.Hour)},
	}
}

func setupKafkaOAuth(t *testing.T, topic string) string {
	t.Helper()
	ctx := context.Background()

	// As in setupKafkaTLS, only the host-mapped "PLAINTEXT" listener is
	// remapped, here to SASL_PLAINTEXT with OAUTHBEARER.
	kafkaContainer, err := kafka.Run(ctx, "confluentinc/confluent-local:7.5.0",
		testcontainers.WithEnv(map[string]string{
			"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP":                       "BROKER:PLAINTEXT,PLAINTEXT:SASL_PLAINTEXT,CONTROLLER:PLAINTEXT",
			"KAFKA_LISTENER_NAME_PLAINTEXT_SASL_ENABLED_MECHANISMS":      "OAUTHBEARER",
			"KAFKA_LISTENER_NAME_PLAINTEXT_OAUTHBEARER_SASL_JAAS_CONFIG": "org.apache.kafka.common.security.oauthbearer.OAuthBearerLoginModule required;",
		}),
	)
	t.Cleanup(func() {
		if kafkaContainer != nil {
			_ = kafkaContainer.Terminate(ctx)
		}
	})
	require.NoError(t, err)

	brokers, err := kafkaContainer.Brokers(ctx)
	require.NoError(t, err)
	broker := brokers[0]

	createKafkaTopicWithDialer(t, oauthTestDialer(t), broker, topic)
	return broker
}
