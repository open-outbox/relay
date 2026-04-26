package relay

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// EventStatus represents the lifecycle stage of an event in the outbox.
type EventStatus string

const (
	// EventStatusPending indicates the event is ready to be picked up by a relay instance.
	EventStatusPending EventStatus = "PENDING"
	// EventStatusDelivering indicates the event is currently locked and being
	// processed by a relay instance.
	EventStatusDelivering EventStatus = "DELIVERING"
	// EventStatusDelivered indicates the event was successfully published to the message broker.
	EventStatusDelivered EventStatus = "DELIVERED"
	// EventStatusDead indicates the event failed delivery attempts beyond the
	// retry limit and is quarantined.
	EventStatusDead EventStatus = "DEAD"
)

// Event represents a single unit of work from the outbox table.
// It contains the message payload, metadata for routing, and delivery tracking information.
type Event struct {
	// ID is the unique identifier for the event (UUID v4/v7 recommended).
	ID uuid.UUID `db:"event_id"      json:"id"`
	// Type defines the subject/topic the message should be published to.
	Type string `db:"event_type"    json:"type"`
	// PartitionKey is used for load balancing on the broker side if supported
	PartitionKey *string `db:"partition_key" json:"partition_key"`
	// Payload is the raw message body.
	Payload []byte `db:"payload"       json:"payload"`
	// Headers is a JSON blob containing custom message attributes/headers.
	Headers json.RawMessage `db:"headers"       json:"headers"`

	// Attempts tracks the number of times this event has been tried for delivery.
	Attempts int `db:"attempts" json:"attempts"`

	// CreatedAt is the timestamp when the event was first inserted into the database.
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// GetPartitionKey safely dereferences the optional PartitionKey.
// It returns the value if present, or an empty string if the value was NULL in the database.
// This prevents nil-pointer panics when publishers access the key.
func (e Event) GetPartitionKey() string {
	if e.PartitionKey == nil {
		return ""
	}
	return *e.PartitionKey
}

// FailedEvent is a container used to report processing failures back to the storage layer.
// It includes the updated status and scheduling information for the next retry.
type FailedEvent struct {
	// ID of the event that failed.
	ID uuid.UUID `json:"id"           db:"id"`
	// NewStatus is the state the event should transition to (PENDING or DEAD).
	NewStatus EventStatus `json:"new_status"   db:"status"`
	// AvailableAt is the time when the event becomes eligible for retry.
	AvailableAt time.Time `json:"available_at" db:"available_at"`
	// Attempts is the incremented count of delivery tries.
	Attempts int `json:"attempts"     db:"attempts"`
	// LastError captures the error message from the publisher for diagnostics.
	LastError string `json:"last_error"   db:"last_error"`
}
