package relay

import (
	"context"
	"fmt"
)

// PublishStatus categorizes the result of a delivery attempt.
type PublishStatus int

const (
	// StatusSuccess indicates the event was successfully accepted by the downstream
	// provider (e.g., Kafka ACK received, HTTP 2xx response).
	StatusSuccess PublishStatus = iota

	// StatusFailed indicates the downstream provider rejected the event or the
	// attempt timed out. Check PublishError for retryability logic.
	StatusFailed
)

// String returns a human-readable representation of the PublishStatus.
// This is used primarily for structured logging (e.g., zap, slog) and debugging.
func (s PublishStatus) String() string {
	switch s {
	case StatusSuccess:
		return "Success"
	case StatusFailed:
		return "Failed"
	default:
		// Returning the underlying integer as a string prevents data loss
		// if a new status is added but the String method isn't updated.
		return fmt.Sprintf("Unknown(%d)", s)
	}
}

// PublishResult contains delivery metadata returned by the downstream provider.
// It is used by the Engine to finalize the event lifecycle in the local database.
type PublishResult struct {
	// Status indicates if the provider successfully acknowledged the event.
	Status PublishStatus

	// ProviderID is the unique identifier assigned by the external system
	// (e.g., Kafka offset, NATS sequence, or HTTP Request-ID).
	// Store this for cross-system traceablity and auditing.
	ProviderID string

	// Metadata holds protocol-specific key-value pairs that are useful for
	// debugging or specialized routing (e.g., {"http_status": "202", "partition": "4"}).
	Metadata map[string]string
}

// AddMetadata is a helper to safely set values in the Metadata map,
// initializing it if it hasn't been created yet.
func (r *PublishResult) AddMetadata(key, value string) {
	if r.Metadata == nil {
		r.Metadata = make(map[string]string)
	}
	r.Metadata[key] = value
}

// IsSuccess returns true if the status is StatusSuccess.
func (r *PublishResult) IsSuccess() bool {
	return r.Status == StatusSuccess
}

// PublishError defines how the Engine should react to a failure.
type PublishError struct {
	Err         error
	IsRetryable bool   // Crucial: Tells the Engine whether to try again or DLQ.
	Code        string // e.g., "BROKER_NACK", "AUTH_EXPIRED", "VALIDATION_ERROR"
}

func (e *PublishError) Error() string { return e.Err.Error() }
func (e *PublishError) Unwrap() error { return e.Err }

// Publisher is the common interface for all egress transports.
type Publisher interface {
	// Publish blocks until the downstream system acknowledges receipt (ACK/NACK).
	Publish(ctx context.Context, event Event) (PublishResult, error)
}
