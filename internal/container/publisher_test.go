package container

import (
	"testing"

	"github.com/open-outbox/relay/internal/config"
	"github.com/open-outbox/relay/internal/publishers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPublisher(t *testing.T) {
	p, err := buildPublisher(&config.Config{PublisherType: "stdout"})
	require.NoError(t, err)
	assert.IsType(t, &publishers.Stdout{}, p)

	_, err = buildPublisher(&config.Config{PublisherType: "rabbitmq"})
	require.EqualError(t, err,
		"unknown publisher type: rabbitmq (supported: kafka, nats, null, redis, stdout)")
}
