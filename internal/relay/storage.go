package relay

import (
	"context"
)

// Storage defines the contract for how the Relay reads and updates events.
type Storage interface {
	// Fetch retrieves a batch of events that are ready to be sent.
	// batchSize prevents the relay from overloading memory.
	Fetch(ctx context.Context, batchSize int) ([]Event, error)

	// MarkDone records that an event was successfully processed.
	// In a real DB, this might delete the row or update 'status' to 'completed'.
	MarkDone(ctx context.Context, id string) error

	// MarkFailed records a failure and increments the retry count.
	MarkFailed(ctx context.Context, id string, reason string) error

	// Gets the status of events 
	GetStats(ctx context.Context) (Stats, error)
}

type Stats struct {
	Pending int `json:"pending"`
	Retrying int `json:"retrying"`
	Failed  int `json:"failed"`
}
