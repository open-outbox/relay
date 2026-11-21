package container

import (
	"testing"

	"github.com/open-outbox/relay/internal/config"
	"github.com/open-outbox/relay/internal/publishers"
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
			require.IsType(t, &publishers.Kafka{}, p)
		})
	}
}
