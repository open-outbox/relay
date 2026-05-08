package relay

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Storage defines the contract for how the Relay reads and updates events.
// Implementations are responsible for managing the persistence of outbox events
type Storage interface {
	// ClaimBatch identifies and locks a set of pending events for processing.
	// It transitions events to the 'DELIVERING' status and ensures they are
	// reserved for this relay instance to prevent duplicate processing.
	//
	// The 'buffer' parameter allows for reusing an existing slice to minimize
	// heap allocations during high-throughput polling.
	ClaimBatch(
		ctx context.Context,
		batchSize int,
		buffer []Event,
	) ([]Event, error)

	// MarkDeliveredBatch moves a set of events to the final 'DELIVERED' state.
	//
	// The implementation must ensure that only events currently locked by
	// this relay instance are updated, preventing race conditions or
	// accidental overrides if a lease was previously reaped.
	MarkDeliveredBatch(ctx context.Context, ids []uuid.UUID) (int64, error)

	// MarkFailedBatch handles events that encountered errors during publishing.
	// It updates event metadata (attempts, last_error) and determines if the event
	// should be retried (PENDING) or quarantined (DEAD).
	MarkFailedBatch(ctx context.Context, failures []FailedEvent) (int64, error)

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
	Close(ctx context.Context) error

	// Ping verifies the connectivity to the underlying database. It should
	// return an error if the storage backend is unreachable or misconfigured.
	Ping(ctx context.Context) error
}

// Stats represents a snapshot of the outbox table's current state.
type Stats struct {
	// PendingCount is the total number of events currently in 'PENDING' status.
	PendingCount int64 `json:"pending_count"`
	// RetryingCount is the number of events in 'PENDING' status that have
	// failed at least once (attempts > 0).
	RetryingCount int64 `json:"retrying_count"`
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
