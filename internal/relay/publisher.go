package relay

import "context"

// Publisher defines the contract for sending an event to its destination.
type Publisher interface {
	// Publish sends out the event.
	Publish(ctx context.Context, event Event) error
}
