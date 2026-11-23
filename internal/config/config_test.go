package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_KafkaTLSVersion(t *testing.T) {
	t.Run("defaults to 1.2", func(t *testing.T) {
		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, "1.2", cfg.KafkaTLSVersion)
	})

	t.Run("reads KAFKA_TLS_VERSION from the environment", func(t *testing.T) {
		t.Setenv("KAFKA_TLS_VERSION", "1.3")

		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, "1.3", cfg.KafkaTLSVersion)
	})
}
