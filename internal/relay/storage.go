package relay

import (
	"context"

	"github.com/google/uuid"
)

// Storage defines the contract for how the Relay reads and updates events.
type Storage interface {
	// ClaimBatch captures a set of events and locks them to this relayID.
	// Returns the events to be processed.
	ClaimBatch(
		ctx context.Context,
		relayID string,
		batchSize int,
		leaseMinutes int,
	) ([]Event, error)

	// MarkDeliveredBatch moves a set of IDs to the final 'DELIVERED' state.
	MarkDeliveredBatch(ctx context.Context, ids []uuid.UUID, relayID string) error

	// MarkFailedBatch handles both Retries (PENDING + Backoff) and Quarantine (DEAD).
	MarkFailedBatch(ctx context.Context, failures []FailedEvent, relayID string) error
}

type Stats struct {
	Pending  int `json:"pending"`
	Retrying int `json:"retrying"`
	InFlight int `json:"in_flight"`
	Failed   int `json:"failed"`
}
