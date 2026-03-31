package relay

import "context"

// PublishStatus represents the high-level outcome of a publish attempt.
type PublishStatus string

const (
	StatusSuccess PublishStatus = "success"
	StatusFailed  PublishStatus = "failed"
)

// PublishResult provides metadata about the delivery attempt.
type PublishResult struct {
	// ProviderID is the ID assigned by the downstream system (e.g., Kafka offset, HTTP Trace ID).
	ProviderID string

	// Status tells the engine if the message is considered "delivered".
	Status PublishStatus

	// IsRetryable indicates if the failure is transient (Network) or terminal (Validation).
	IsRetryable bool

	// Metadata allows for transport-specific info (e.g., MQTT Topic, HTTP Status Code).
	Metadata map[string]string
}

// Publisher defines the contract for sending an event to its destination.
type Publisher interface {
	Publish(ctx context.Context, event Event) (PublishResult, error)
}
