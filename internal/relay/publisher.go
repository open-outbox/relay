package relay

import "context"

// Publisher defines the contract for sending an event to its destination.
type Publisher interface {
	// Publish sends the payload to the specified topic.
	Publish(ctx context.Context, event Event) error
}