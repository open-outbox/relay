package publishers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/open-outbox/relay/internal/relay"
	"github.com/redis/go-redis/v9"
)

// Redis is a publisher that pushes events into Redis Streams using the XADD command.
// It implements the relay.Publisher interface.
type Redis struct {
	client *redis.Client
}

// NewRedis establishes a connection to a Redis server.
// It validates the connection with a Ping before returning.
// It accepts redis URLs like "redis://<user>:<pass>@localhost:6379/0" .
func NewRedis(
	url string,
	writeTimeout time.Duration,
	connectionTimeout time.Duration,
) (*Redis, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("invalid redis url: %w", err)
	}

	opts.WriteTimeout = writeTimeout
	opts.ReadTimeout = writeTimeout

	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis connection failed: %w", err)
	}

	return &Redis{client: client}, nil
}

// Publish appends the event to a Redis Stream.
// It uses the Event.Type as the stream name. The event ID and Payload are
// stored as fields within the stream entry.
//
// If publishing fails, it classifies the error as retryable if it indicates
// a transient issue like network disruption or a cluster resharding event.
func (r *Redis) Publish(ctx context.Context, event relay.Event) error {
	values := map[string]interface{}{
		"id":      event.ID.String(),
		"payload": event.Payload,
	}

	if pkey := event.GetPartitionKey(); pkey != "" {
		values["partition_key"] = pkey
	}

	if len(event.Headers) > 0 {
		var hMap map[string]string
		if err := json.Unmarshal(event.Headers, &hMap); err != nil {
			return &relay.PublishError{
				Err:         fmt.Errorf("invalid headers json: %w", err),
				IsRetryable: false,
				Code:        "INVALID_HEADERS",
			}
		}
		for k, v := range hMap {
			// Prefixing with 'h:'
			values["h:"+k] = v
		}
	}

	err := r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: event.Type,
		Values: values,
	}).Err()

	if err != nil {
		return &relay.PublishError{
			Err:         fmt.Errorf("failed to publish to redis stream: %w", err),
			IsRetryable: r.isRedisErrorRetryable(err),
			Code:        "REDIS_PUBLISH_ERROR",
		}
	}

	return nil
}

// isRedisErrorRetryable evaluates whether a Redis error is transient and warrants a retry.
// It handles connection issues, pool timeouts, and server-side states (OOM, Loading, ReadOnly).
// It also specifically handles Redis Cluster redirection errors (MOVED, ASK) which are
// considered retryable as the client will automatically update its slot map.
func (r *Redis) isRedisErrorRetryable(err error) bool {

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
	return errors.As(err, &netErr)
}

// Close gracefully shuts down the Redis client and its underlying connection pool.
func (r *Redis) Close(_ context.Context) error {
	if r.client == nil {
		return nil
	}

	// go-redis/v9's Close() returns an error if the client
	// is already closed or if there's an issue closing the pool.
	if err := r.client.Close(); err != nil {
		return fmt.Errorf("failed to close redis client: %w", err)
	}

	return nil
}

// Ping verifies the connectivity to the Redis server. It ensures the
// client can communicate with the storage backend and that the
// connection pool is healthy.
func (r *Redis) Ping(ctx context.Context) error {
	if r.client == nil {
		return fmt.Errorf("redis client not initialized")
	}

	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping failed: %w", err)
	}

	return nil
}
