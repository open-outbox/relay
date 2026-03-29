package publishers

import (
	"context"
	"fmt"
	"os"

	"github.com/open-outbox/relay/internal/relay"
)

// Stdout represents a publisher that writes to the console.
type Stdout struct{}

// NewStdout creates a new instance of the console publisher.
func NewStdout() *Stdout {
	return &Stdout{}
}

// Publish satisfies the relay.Publisher interface.
// It simply writes the bytes to the standard output.
func (s *Stdout) Publish(ctx context.Context, event relay.Event) error {
	_, err := fmt.Fprintf(os.Stdout, "TOPIC: %s | PAYLOAD: %s\n", event.Topic, string(event.Payload))
	return err
}