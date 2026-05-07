//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/open-outbox/relay/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/consul"
)

func TestLoad_RemoteConsul(t *testing.T) {
	ctx := context.Background()

	// 1. Start Consul Container
	consulContainer, err := consul.Run(ctx, "hashicorp/consul:1.15")
	require.NoError(t, err)

	t.Cleanup(func() {
		consulContainer.Terminate(ctx)
	})

	endpoint, err := consulContainer.ApiEndpoint(ctx)
	require.NoError(t, err)

	url := fmt.Sprintf("http://%s/v1/kv/%s", endpoint, "config/relay.yaml")

	configYaml := `
BATCH_SIZE: 555
PUBLISHER_TYPE: kafka
`
	seedConsul(t, endpoint, url, configYaml)

	t.Setenv("REMOTE_CONFIG_PROVIDER", "consul")
	t.Setenv("REMOTE_CONFIG_ENDPOINT", endpoint)
	t.Setenv("REMOTE_CONFIG_PATH", "config/relay.yaml")
	t.Setenv("REMOTE_CONFIG_TYPE", "yaml")

	cfg, err := config.Load()

	assert.NoError(t, err)
	assert.Equal(t, 555, cfg.BatchSize)
	assert.Equal(t, "kafka", cfg.PublisherType)
}

func TestLoad_RemoteConsul_KeyNotFound(t *testing.T) {
	ctx := context.Background()

	consulContainer, err := consul.Run(ctx, "hashicorp/consul:1.15")
	require.NoError(t, err)
	defer consulContainer.Terminate(ctx)

	endpoint, _ := consulContainer.ApiEndpoint(ctx)

	// 2. Point to a path that we NEVER seeded
	t.Setenv("REMOTE_CONFIG_PROVIDER", "consul")
	t.Setenv("REMOTE_CONFIG_ENDPOINT", endpoint)
	t.Setenv("REMOTE_CONFIG_PATH", "non/existent/path.yaml")

	cfg, err := config.Load()

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read remote config")
	assert.Nil(t, cfg)
}

func seedConsul(t *testing.T, endpoint string, url string, yamlData string) {
	// url := fmt.Sprintf("http://%s/v1/kv/%s", endpoint, path)

	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(yamlData))
	require.NoError(t, err, "Failed to create HTTP request for seeding")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "Failed to execute HTTP request to Consul")
	defer resp.Body.Close()

	// Assert that Consul accepted the data
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Consul KV store did not return 200 OK")
}
