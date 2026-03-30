package publishers

import (
	"context"

	"github.com/open-outbox/relay/internal/relay"
)

// Stdout represents a publisher that writes to the console.
type Null struct{}

// NewStdout creates a new instance of the console publisher.
func NewNull() *Null {
	return &Null{}
}

// Publish satisfies the relay.Publisher interface.
// It simply writes the bytes to the standard output.
func (s *Null) Publish(ctx context.Context, event relay.Event) error {
	return nil
}
