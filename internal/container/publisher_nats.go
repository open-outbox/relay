package container

import (
	"github.com/open-outbox/relay/internal/config"
	"github.com/open-outbox/relay/internal/publishers"
	"github.com/open-outbox/relay/internal/relay"
)

func buildNatsPublisher(cfg config.Config) (relay.Publisher, error) {
	return publishers.NewNats(
		cfg.PublisherURL,
		cfg.NatsPublishTimeout,
		cfg.NatsConnectionTimeout,
	)
}
