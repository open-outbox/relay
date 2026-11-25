//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/open-outbox/relay/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_RemoteConsul(t *testing.T) {
	endpoint := sharedConsul.get(t)
	key := consulKey(t)

	seedConsul(t, endpoint, key, `
BATCH_SIZE: 555
PUBLISHER_TYPE: kafka
`)
	setRemoteConsulEnv(t, endpoint, key)

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, 555, cfg.BatchSize)
	assert.Equal(t, "kafka", cfg.PublisherType)
}

func TestLoad_RemoteConsul_KeyNotFound(t *testing.T) {
	endpoint := sharedConsul.get(t)

	// The key is unique to this test and never seeded.
	setRemoteConsulEnv(t, endpoint, consulKey(t))

	cfg, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read remote config")
	assert.Nil(t, cfg)
}

// consulKey returns a key unique to the test, so tests sharing the Consul
// container cannot see each other's config.
func consulKey(t *testing.T) string {
	return "relay-tests/" + t.Name() + "/relay.yaml"
}

func setRemoteConsulEnv(t *testing.T, endpoint, key string) {
	t.Helper()
	t.Setenv("REMOTE_CONFIG_PROVIDER", "consul")
	t.Setenv("REMOTE_CONFIG_ENDPOINT", endpoint)
	t.Setenv("REMOTE_CONFIG_PATH", key)
	t.Setenv("REMOTE_CONFIG_TYPE", "yaml")
}

func seedConsul(t *testing.T, endpoint, key, yamlData string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPut, "http://"+endpoint+"/v1/kv/"+key, strings.NewReader(yamlData),
	)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "failed to reach Consul")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "Consul did not store the config")
}
