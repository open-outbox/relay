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

// Publish satisfies the relay.Publisher interface.
// It effectively "black holes" the event and immediately reports success.
func (s *Null) Publish(ctx context.Context, event relay.Event) (relay.PublishResult, error) {
	// For performance testing, we return a successful result immediately.
	// We use the Event ID as the ProviderID to simulate a real audit trail.
	return relay.PublishResult{
		Status:     relay.StatusSuccess,
		ProviderID: event.ID.String(),
		Metadata:   nil,
	}, nil
}
