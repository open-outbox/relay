package relay

import (
	"context"
)

// PublishError is a specialized error type used by Publisher implementations
// to communicate the nature of a failure back to the Engine.
type PublishError struct {
	// Err is the underlying error returned by the transport client.
	Err error
	// IsRetryable indicates if the failure is transient (e.g., network timeout)
	// or permanent (e.g., authentication failure, invalid subject).
	// This field determines whether the Engine will retry the event or move it to 'DEAD' status.
	IsRetryable bool
	// Code is a machine-readable string used for categorizing errors in metrics and logs.
	Code string // e.g., "BROKER_NACK", "AUTH_EXPIRED", "VALIDATION_ERROR"
}

func (e *PublishError) Error() string { return e.Err.Error() }
func (e *PublishError) Unwrap() error { return e.Err }

// Publisher is the common interface for all egress transports.
// It abstracts the specifics of message brokers (Kafka, NATS, Redis, etc.)
// providing a unified contract for the Relay Engine.
type Publisher interface {

	// Connect establishes the initial connection to the message broker.
	// It should perform handshakes, authenticate, and initialize necessary
	// resources (like NATS JetStream contexts or Kafka writers).
	Connect(ctx context.Context) error

	// Publish sends a single event to the downstream system.
	// It should block until the broker provides a delivery acknowledgment
	// or the provided context is cancelled.
	Publish(ctx context.Context, event Event) error

	// Close performs a graceful shutdown of the publisher, ensuring any
	// buffered data is flushed and network resources are released.
	Close(ctx context.Context) error

	// Ping verifies the connectivity to the message broker (e.g., Kafka, NATS).
	// It ensures the publisher is authenticated and capable of sending messages.
	Ping(ctx context.Context) error
}
