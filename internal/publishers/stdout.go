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

// Publish satisfies the relay.Publisher interface.
// It formats the event as a string and writes it to standard output.
func (s *Stdout) Publish(ctx context.Context, event relay.Event) error {
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

func (s *Stdout) Close() error {
	return nil
}
