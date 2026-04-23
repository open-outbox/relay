package publishers

import (
	"context"

	"github.com/open-outbox/relay/internal/relay"
)

// Null represents a publisher that does nothing. It is primarily used for
// performance benchmarking the engine's orchestration and storage overhead
// without transport latency.
type Null struct{}

// NewNull creates a new instance of the Null publisher.
func NewNull() *Null {
	return &Null{}
}

// Connect satisfies the relay.Publisher interface.
// For the Null publisher, this is a no-op that always reports success,
// allowing the engine to start immediately without a physical broker.
func (n *Null) Connect(_ context.Context) error {
	return nil
}

// Publish satisfies the relay.Publisher interface.
// It effectively "black holes" the event and immediately reports success.
func (n *Null) Publish(_ context.Context, _ relay.Event) error {
	return nil
}

// Close does nothing on the Null publisher
func (n *Null) Close(_ context.Context) error {
	return nil
}

// Ping does nothing on the Null publisher
func (n *Null) Ping(_ context.Context) error {
	return nil
}
