package relay

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Storage defines the contract for how the Relay reads and updates events.
// Implementations are responsible for managing the persistence of outbox events
type Storage interface {
	// ClaimBatch identifies and locks a set of pending events for a specific relay instance.
	// It transitions events to the 'DELIVERING' status and associates them with the relayID.
	// The 'buffer' parameter allows for reusing a slice to minimize allocations.
	ClaimBatch(
		ctx context.Context,
		relayID string,
		batchSize int,
		buffer []Event,
	) ([]Event, error)

	// MarkDeliveredBatch moves events to the final 'DELIVERED' state.
	// It must verify that the events are still locked by the provided relayID
	// to prevent race conditions with the lease reaper.
	MarkDeliveredBatch(ctx context.Context, ids []uuid.UUID, relayID string) error

	// MarkFailedBatch handles events that encountered errors during publishing.
	// It updates event metadata (attempts, last_error) and determines if the event
	// should be retried (PENDING) or quarantined (DEAD).
	MarkFailedBatch(ctx context.Context, failures []FailedEvent, relayID string) error

	// ReapExpiredLeases identifies events stuck in the 'DELIVERING' state past their
	// lease duration and resets them to 'PENDING', allowing other instances to pick them up.
	ReapExpiredLeases(ctx context.Context, leaseTimeout time.Duration, limit int) (int64, error)

	// GetStats retrieves high-level operational metrics about the outbox table,
	// such as the current backlog size and the age of the oldest pending message.
	GetStats(ctx context.Context) (Stats, error)

	// Prune removes old DELIVERED and DEAD events from storage to maintain performance.
	// This is typically called by the CLI or a background maintenance job.
	Prune(ctx context.Context, opts PruneOptions) (PruneResult, error)

	// Close releases any resources held by the storage implementation, such as
	// database connection pools.
	Close() error
}

// Stats represents a snapshot of the outbox table's current state.
type Stats struct {
	// PendingCount is the total number of events currently in 'PENDING' status.
	PendingCount int64 `json:"pending_count"`
	// OldestAgeSec is the age in seconds of the oldest event waiting to be processed.
	OldestAgeSec int64 `json:"oldest_age_sec"`
}

// PruneOptions defines the criteria for cleaning up old records.
type PruneOptions struct {
	// DeliveredAge defines the duration threshold for DELIVERED events.
	// The string must follow the format "[number][unit]" where unit is:
	// 'd' for days, 'h' for hours, or 'm' for minutes (e.g., "7d", "24h", "60m").
	// An empty string or "0" indicates that no pruning should be performed
	// for this status.
	DeliveredAge string

	// DeadAge defines the duration threshold for DEAD events.
	// Follows the same format as DeliveredAge (e.g., "30d").
	// Use this to clear out "quarantined" events after a period of time.
	DeadAge string

	// DryRun, if true, instructs the storage implementation to calculate
	// and return the count of rows that meet the criteria without
	// actually performing the deletion.
	DryRun bool
}

// PruneResult provides feedback on the cleanup operation, returning the
// number of records affected by the maintenance task.
type PruneResult struct {
	// DeliveredDeleted is the total number of events with status 'DELIVERED'
	// that were successfully removed from the storage.
	DeliveredDeleted int64

	// DeadDeleted is the total number of events with status 'DEAD'
	// that were successfully removed from the storage.
	DeadDeleted int64
}
