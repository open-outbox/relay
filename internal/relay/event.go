package relay

import (
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusPending    Status = "PENDING"    // Ready to be picked up
	StatusDelivering Status = "DELIVERING" // Currently being processed (Locked)
	StatusDelivered  Status = "DELIVERED"  // Success!
	StatusDead       Status = "DEAD"       // Failed too many times
)

type Event struct {
	ID           uuid.UUID      `db:"event_id"      json:"id"`
	Type         string         `db:"event_type"    json:"type"`
	PartitionKey string         `db:"partition_key" json:"partition_key"`
	Payload      []byte         `db:"payload"       json:"payload"`
	Headers      map[string]any `db:"headers"       json:"headers"`

	// State
	Status    string `db:"status"        json:"status"` // Treat as Read-Only in Go
	Attempts  int    `db:"attempts"      json:"attempts"`
	LastError string `db:"last_error"    json:"last_error"`

	// Time fields
	CreatedAt time.Time `db:"created_at"    json:"created_at"`
	UpdatedAt time.Time `db:"updated_at"    json:"updated_at"`
}
