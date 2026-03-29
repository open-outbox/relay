package publishers

import (
	"context"
	"fmt"
	"strings"

	"github.com/open-outbox/relay/internal/relay"
	"github.com/redis/go-redis/v9"
)

type Redis struct {
	client *redis.Client
}

func NewRedis(url string) (*Redis, error) {
	// go-redis handles "redis://<user>:<pass>@localhost:6379/0" automatically
	opts, err := redis.ParseURL(strings.TrimPrefix(url, "redis://"))
	if err != nil {
		// Fallback for simple "localhost:6379"
		return nil, fmt.Errorf("invalid redis url: %w", err)
	}

	client := redis.NewClient(opts)

	// Ping to ensure connection is alive
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("redis connection failed: %w", err)
	}

	return &Redis{client: client}, nil
}

func (r *Redis) Publish(ctx context.Context, event relay.Event) error {
	// XADD appends the message to a stream
	// Type = Stream Name
	// Values = Map of data
	return r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: event.Type,
		Values: map[string]interface{}{
			"id":      event.ID.String(),
			"payload": event.Payload,
		},
	}).Err()
}

func (r *Redis) Close() error {
	return r.client.Close()
}
