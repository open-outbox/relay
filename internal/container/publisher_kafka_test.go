package container

import (
	"context"
	"fmt"
	"testing"

	"github.com/open-outbox/relay/internal/config"
	kafkapub "github.com/open-outbox/relay/internal/publishers/kafka"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildKafkaPublisher_Config(t *testing.T) {
	base := config.Config{
		PublisherURL:      "kafka://localhost:9092",
		KafkaCompression:  "none",
		KafkaRequiredAcks: "all",
		KafkaInsecure:     true, // enables TLS so MinVersion is observable
	}

	t.Run("rejects unknown versions", func(t *testing.T) {
		cfg := base
		cfg.KafkaTLSVersion = "1.4"

		_, err := buildKafkaPublisher(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported Kafka TLS version")
	})

	for _, m := range []string{"PLAIN", "Scram-SHA-256", " scram-sha-512 "} {
		t.Run("accepts SASL mechanism "+m, func(t *testing.T) {
			cfg := base
			cfg.KafkaSASLMechanism = m
			cfg.KafkaUsername = "user"
			cfg.KafkaPassword = "pass"

			_, err := buildKafkaPublisher(cfg)
			require.NoError(t, err)
		})
	}

	for _, v := range []string{"", "1.2", "1.3"} {
		t.Run("accepts "+v, func(t *testing.T) {
			cfg := base
			cfg.KafkaTLSVersion = v

			p, err := buildKafkaPublisher(cfg)
			require.NoError(t, err)
			require.IsType(t, &kafkapub.Publisher{}, p)
		})
	}
}

func TestBuildKafkaPublisher_Partitioner(t *testing.T) {
	base := config.Config{
		PublisherURL:      "kafka://localhost:9092",
		KafkaCompression:  "none",
		KafkaRequiredAcks: "all",
	}

	for _, in := range []string{"", "fnv1a", " FNV1A ", "murmur2", "Murmur2", "crc32", " CRC32 \t"} {
		t.Run(fmt.Sprintf("accepts %q", in), func(t *testing.T) {
			cfg := base
			cfg.KafkaPartitioner = in

			p, err := buildKafkaPublisher(cfg)
			require.NoError(t, err)
			require.IsType(t, &kafkapub.Publisher{}, p)
		})
	}

	t.Run("rejects unknown partitioners", func(t *testing.T) {
		cfg := base
		cfg.KafkaPartitioner = "xxhash"

		_, err := buildKafkaPublisher(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported Kafka partitioner: xxhash")
	})
}

func TestParseKafkaBrokers(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want []string
	}{
		{"single broker", "kafka://a:9092", []string{"a:9092"}},
		{"without scheme", "a:9092,b:9092", []string{"a:9092", "b:9092"}},
		{
			"spaces around entries",
			"kafka://a:9092, b:9092 ,\tc:9092",
			[]string{"a:9092", "b:9092", "c:9092"},
		},
		{"scheme on every entry", "kafka://a:9092,kafka://b:9092", []string{"a:9092", "b:9092"}},
		{"empty entries are skipped", "kafka://a:9092,, ,b:9092,", []string{"a:9092", "b:9092"}},
		{"empty url", "", nil},
		{"scheme only", "kafka://", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseKafkaBrokers(tt.url))
		})
	}
}

func TestBuildKafkaPublisher_NoBrokers(t *testing.T) {
	_, err := buildKafkaPublisher(config.Config{PublisherURL: "kafka:// , "})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no broker addresses provided")
}

func TestBuildKafkaPublisher_RejectsUnknownOptions(t *testing.T) {
	base := config.Config{
		PublisherURL:      "kafka://localhost:9092",
		KafkaCompression:  "none",
		KafkaRequiredAcks: "all",
	}

	tests := map[string]struct {
		set     func(*config.Config)
		wantErr string
	}{
		"compression": {
			set:     func(c *config.Config) { c.KafkaCompression = "brotli" },
			wantErr: "unsupported Kafka compression: brotli",
		},
		"required acks": {
			set:     func(c *config.Config) { c.KafkaRequiredAcks = "2" },
			wantErr: "unsupported Kafka required acks: 2",
		},
		"SASL mechanism": {
			set:     func(c *config.Config) { c.KafkaSASLMechanism = "gssapi" },
			wantErr: "unsupported Kafka SASL mechanism: gssapi",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := base
			tt.set(&cfg)

			_, err := buildKafkaPublisher(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestParseKafkaOAuthExtensions(t *testing.T) {
	got, err := parseKafkaOAuthExtensions(" logicalCluster=lkc-1 , identityPoolId=pool-2,")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"logicalCluster": "lkc-1", "identityPoolId": "pool-2"}, got)

	got, err = parseKafkaOAuthExtensions("")
	require.NoError(t, err)
	assert.Nil(t, got)

	for _, in := range []string{"logicalCluster", "=lkc-1"} {
		_, err := parseKafkaOAuthExtensions(in)
		require.Error(t, err, in)
		assert.Contains(t, err.Error(), "expected key=value")
	}
}

func TestBuildKafkaPublisher_OAuthBearer(t *testing.T) {
	cfg := config.Config{
		PublisherURL:           "kafka://localhost:9092",
		KafkaCompression:       "none",
		KafkaRequiredAcks:      "all",
		KafkaSASLMechanism:     "OAUTHBEARER",
		KafkaOAuthTokenURL:     "https://idp.example/token",
		KafkaOAuthClientID:     "relay",
		KafkaOAuthClientSecret: "secret",
		KafkaOAuthScopes:       "kafka, produce",
		KafkaOAuthExtensions:   "logicalCluster=lkc-1",
	}

	p, err := buildKafkaPublisher(cfg)
	require.NoError(t, err)
	require.IsType(t, &kafkapub.Publisher{}, p)

	cfg.KafkaOAuthExtensions = "logicalCluster"
	_, err = buildKafkaPublisher(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KAFKA_OAUTH_EXTENSIONS")
}

func TestBuildKafkaPublisher_MetadataScope(t *testing.T) {
	cfg := config.Config{
		PublisherURL:        "kafka://localhost:9092",
		KafkaCompression:    "none",
		KafkaRequiredAcks:   "all",
		KafkaMetadataTopics: " orders.v1, ,payments.v1 ",
	}

	p, err := buildKafkaPublisher(cfg)
	require.NoError(t, err)

	// The list reaches the publisher: an unlisted topic is rejected before
	// any write, so the broker address is never dialled.
	err = p.Publish(context.Background(), relay.Event{Type: "other.v1"})
	var pubErr *relay.PublishError
	require.ErrorAs(t, err, &pubErr)
	assert.Equal(t, "TOPIC_NOT_IN_METADATA", pubErr.Code)
}
