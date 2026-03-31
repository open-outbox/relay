package publishers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/open-outbox/relay/internal/relay"
	"github.com/redis/go-redis/v9"
)

// Redis is a publisher implementation that pushes events into Redis Streams (XADD).
type Redis struct {
	client *redis.Client
}

// NewRedis establishes a connection to a Redis server.
// It accepts redis URLs like "redis://<user>:<pass>@localhost:6379/0" .
func NewRedis(url string) (*Redis, error) {
	opts, err := redis.ParseURL(strings.TrimPrefix(url, "redis://"))
	if err != nil {
		return nil, fmt.Errorf("invalid redis url: %w", err)
	}

	client := redis.NewClient(opts)

	//TODO: Configure the timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis connection failed: %w", err)
	}

	return &Redis{client: client}, nil
}

// Publish satisfies the relay.Publisher interface.
// Publish appends the event to a Redis Stream using the event type as the Stream name.
func (r *Redis) Publish(ctx context.Context, event relay.Event) (relay.PublishResult, error) {

	err := r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: event.Type,
		Values: map[string]interface{}{
			"id":      event.ID.String(),
			"payload": event.Payload,
		},
	}).Err()

	if err != nil {
		return relay.PublishResult{}, &relay.PublishError{
			Err:         fmt.Errorf("failed to publish to redis stream: %w", err),
			IsRetryable: r.isRetryable(err),
			Code:        "REDIS_PUBLISH_ERROR",
		}
	}

	return relay.PublishResult{
		Status:     relay.StatusSuccess,
		ProviderID: event.ID.String(),
	}, nil
}

func (r *Redis) isRetryable(err error) bool {

	if err == nil {
		return false
	}

	if isContextError(err) {
		return true
	}

	// IO / Connectivity
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// Pool Issues (Specifically check PoolTimeout)
	// If the pool is full, we should back off and try again.
	if errors.Is(err, redis.ErrPoolExhausted) || errors.Is(err, redis.ErrPoolTimeout) {
		return true
	}

	// Server-Side Transient States (Using go-redis built-in checks)
	if redis.IsLoadingError(err) ||
		redis.IsReadOnlyError(err) ||
		redis.IsClusterDownError(err) ||
		redis.IsMasterDownError(err) ||
		redis.IsTryAgainError(err) ||
		redis.IsOOMError(err) ||
		redis.IsNoReplicasError(err) {
		return true
	}

	// Cluster Redirection
	// MOVED and ASK mean the cluster is resharding.
	// The client will update its slot map, so a retry will hit the right node.
	if _, ok := redis.IsMovedError(err); ok {
		return true
	}
	if _, ok := redis.IsAskError(err); ok {
		return true
	}

	// 6. Generic Network fallback
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	return false
}

// Close gracefully shuts down the Redis client and its connection pool.
func (r *Redis) Close() error {
	if r.client != nil {
		return r.client.Close()
	}
	return nil
}
