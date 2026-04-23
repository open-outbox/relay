package publishers

import (
	"context"
	"fmt"
	"os"

	"github.com/open-outbox/relay/internal/relay"
)

// Stdout represents a publisher that writes event data to the console.
// It is primarily used for local development, debugging, and piping
// output to other CLI tools.
type Stdout struct{}

// NewStdout creates a new instance of the Stdout publisher.
func NewStdout() *Stdout {
	return &Stdout{}
}

// Connect satisfies the relay.Publisher interface.
// For the Stdout publisher, this is a no-op that always reports success,
// as the standard output stream is assumed to be available at runtime.
func (s *Stdout) Connect(_ context.Context) error {
	return nil
}

// Publish satisfies the relay.Publisher interface.
// It formats the event as a string and writes it to standard output.
func (s *Stdout) Publish(_ context.Context, event relay.Event) error {
	headersStr := "{}"
	if len(event.Headers) > 0 {
		headersStr = string(event.Headers)
	}
	_, err := fmt.Fprintf(os.Stdout, "ID: %s | TYPE: %s | PAYLOAD: %s | HEADERS %s\n",
		event.ID,
		event.Type,
		string(event.Payload),
		headersStr,
	)

	if err != nil {
		return &relay.PublishError{
			Err:         fmt.Errorf("failed to write to stdout: %w", err),
			IsRetryable: false,
			Code:        "STDOUT_WRITE_FAIL",
		}
	}

	return nil
}

// Close does nothing on the Stdout publisher
func (s *Stdout) Close(_ context.Context) error {
	return nil
}

// Ping does nothing on the Stdout publisher
func (s *Stdout) Ping(_ context.Context) error {
	return nil
}
