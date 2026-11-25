package container

import (
	"github.com/open-outbox/relay/internal/config"
	"github.com/open-outbox/relay/internal/publishers"
	"github.com/open-outbox/relay/internal/relay"
)

func buildRedisPublisher(cfg config.Config) (relay.Publisher, error) {
	return publishers.NewRedis(
		cfg.PublisherURL,
		cfg.RedisConnectionTimeout,
		cfg.RedisWriteTimeout,
	)
}
