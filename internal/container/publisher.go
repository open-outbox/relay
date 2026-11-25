package container

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/open-outbox/relay/internal/config"
	"github.com/open-outbox/relay/internal/publishers"
	"github.com/open-outbox/relay/internal/relay"
)

type publisherBuilder func(cfg config.Config) (relay.Publisher, error)

// publisherBuilders maps PUBLISHER_TYPE to the function that builds that
// publisher. Adding a broker means adding its builder here.
var publisherBuilders = map[string]publisherBuilder{
	"kafka": buildKafkaPublisher,
	"nats":  buildNatsPublisher,
	"redis": buildRedisPublisher,
	"stdout": func(config.Config) (relay.Publisher, error) {
		return publishers.NewStdout(), nil
	},
	"null": func(config.Config) (relay.Publisher, error) {
		return publishers.NewNull(), nil
	},
}

func buildPublisher(cfg *config.Config) (relay.Publisher, error) {
	build, ok := publisherBuilders[cfg.PublisherType]
	if !ok {
		return nil, fmt.Errorf(
			"unknown publisher type: %s (supported: %s)",
			cfg.PublisherType,
			strings.Join(slices.Sorted(maps.Keys(publisherBuilders)), ", "),
		)
	}
	return build(*cfg)
}
