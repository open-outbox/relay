package relay

import (
	"time"

	"github.com/google/uuid"
)

type Event struct {
	ID        uuid.UUID `db:"id"         json:"id"`
	Topic     string    `db:"topic"      json:"topic"`
	Payload   []byte    `db:"payload"    json:"payload"`
	Status    string    `db:"status"     json:"status"`
	Retries   int       `db:"retry_count" json:"retry_count"`   // Mapping retry_count
	LastError string    `db:"last_error"  json:"last_error"`    // Mapping last_error
	CreatedAt time.Time `db:"created_at"  json:"created_at"`
	UpdatedAt time.Time `db:"updated_at"  json:"updated_at"`
}