package publishers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/open-outbox/relay/internal/relay"
)

// Nats is a JetStream-powered publisher for At-Least-Once delivery.
type Nats struct {
	conn           *nats.Conn
	js             nats.JetStreamContext
	publishTimeout time.Duration
}

// NewNats establishes a JetStream connection.
func NewNats(url string, publishTimeout time.Duration) (*Nats, error) {
	nc, err := nats.Connect(url, nats.Name("OpenOutbox-Relay"))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("failed to load JetStream: %w", err)
	}

	return &Nats{conn: nc, js: js, publishTimeout: publishTimeout}, nil
}

// Publish ensures the message is persisted in NATS JetStream before returning.
func (n *Nats) Publish(ctx context.Context, event relay.Event) error {
	if n.conn.IsClosed() {
		return &relay.PublishError{
			Err:         errors.New("nats: connection closed permanently"),
			IsRetryable: true,
			Code:        "NATS_FATAL_CLOSED",
		}
	}

	msg := nats.NewMsg(event.Type)
	msg.Data = event.Payload

	if len(event.Headers) > 0 {
		var hMap map[string]string
		if err := json.Unmarshal(event.Headers, &hMap); err != nil {
			return &relay.PublishError{
				Err:         fmt.Errorf("invalid headers json: %w", err),
				IsRetryable: false,
				Code:        "INVALID_HEADERS",
			}
		}
		for k, v := range hMap {
			msg.Header.Set(k, v)
		}
	}

	msg.Header.Set("Nats-Msg-Id", event.ID.String())

	if event.PartitionKey != "" {
		msg.Header.Set("X-Partition-Key", event.PartitionKey)
	}

	pubCtx, cancel := context.WithTimeout(ctx, n.publishTimeout*time.Second)
	defer cancel()

	_, err := n.js.PublishMsg(msg, nats.Context(pubCtx))

	if err != nil {
		return &relay.PublishError{
			Err:         fmt.Errorf("jetstream publish failed: %w", err),
			IsRetryable: isNatsErrorRetryable(err),
			Code:        "NATS_JS_PUBLISH_ERROR",
		}
	}

	return nil
}

func (n *Nats) Close() error {
	if n.conn != nil {
		n.conn.Close()
	}
	return nil
}

func isNatsErrorRetryable(err error) bool {

	if err == nil {
		return false
	}

	if isContextError(err) {
		return true
	}

	// Handle JetStream API Errors
	var jsErr jetstream.JetStreamError
	if errors.As(err, &jsErr) {
		apiErr := jsErr.APIError()
		if apiErr != nil {
			switch apiErr.ErrorCode {
			case jetstream.JSErrCodeStreamNotFound,
				jetstream.JSErrCodeJetStreamNotEnabled,
				jetstream.JSErrCodeJetStreamNotEnabledForAccount,
				jetstream.JSErrCodeBadRequest:
				return false
			}
		}
	}

	switch {
	// --- Security & Permissions (Human intervention required) ---
	case errors.Is(err, nats.ErrAuthorization),
		errors.Is(err, nats.ErrAuthExpired),
		errors.Is(err, nats.ErrPermissionViolation),
		errors.Is(err, nats.ErrAccountAuthExpired):
		return false
	// --- Logic & Argument Errors (Code bugs) ---
	case errors.Is(err, nats.ErrBadSubject),
		errors.Is(err, nats.ErrInvalidMsg),
		errors.Is(err, nats.ErrInvalidArg),
		errors.Is(err, nats.ErrBadTimeout),
		errors.Is(err, nats.ErrJsonParse):
		return false

	// --- Protocol/Payload Limits (Requires server config change) ---
	case errors.Is(err, nats.ErrMaxPayload),
		errors.Is(err, nats.ErrHeadersNotSupported),
		errors.Is(err, nats.ErrBadHeaderMsg),
		errors.Is(err, nats.ErrNkeysNotSupported):
		return false
	}

	return true
}
