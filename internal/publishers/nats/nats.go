// Package nats publishes relay events to NATS JetStream subjects.
package nats

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/utils"
)

// Publisher is a JetStream-powered publisher for At-Least-Once delivery.
// It implements the relay.Publisher interface by publishing messages to NATS subjects
// that are backed by a JetStream stream for durability.
type Publisher struct {
	url               string
	connectionTimeout time.Duration
	publishTimeout    time.Duration

	mu      sync.Mutex
	closed  bool
	session atomic.Pointer[session]
}

// session is one NATS connection and the JetStream context built on it.
// They are stored together, so a reader never sees one without the other.
type session struct {
	conn *nats.Conn
	js   nats.JetStreamContext
}

var (
	errNotConnected    = errors.New("nats publisher is not connected")
	errPublisherClosed = errors.New("nats publisher is closed")
)

// New builds a new NATS publisher. No connection is opened here; call Connect.
func New(url string, publishTimeout, connectionTimeout time.Duration) (*Publisher, error) {

	if url == "" {
		return nil, errors.New("nats url is required")
	}

	return &Publisher{
		url:               url,
		publishTimeout:    publishTimeout,
		connectionTimeout: connectionTimeout,
	}, nil
}

// Connect opens a connection to the NATS server. The engine calls it at
// startup and again whenever Ping fails, so it handles three cases:
//
//   - No connection yet, or the current one is closed (nats.go stopped
//     reconnecting and will never use it again): open a new connection.
//   - A connection that is connected or reconnecting: do nothing. nats.go
//     reconnects by itself, and opening a second one would leak the first.
//   - Close was called: return an error, so a health check during shutdown
//     cannot reopen the connection.
//
// The connection is named "Open-Outbox-Relay" in NATS monitoring tools.
func (n *Publisher) Connect(_ context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.closed {
		return errPublisherClosed
	}
	if s := n.session.Load(); s != nil && !s.conn.IsClosed() {
		return nil
	}

	nc, err := nats.Connect(
		n.url,
		nats.Timeout(n.connectionTimeout),
		nats.Name("Open-Outbox-Relay"),
		nats.MaxReconnects(-1),
		nats.IgnoreAuthErrorAbort(),
	)
	if err != nil {
		return fmt.Errorf("nats connection failed: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return fmt.Errorf("failed to load JetStream: %w", err)
	}
	n.session.Store(&session{conn: nc, js: js})
	return nil
}

// Publish sends an event to NATS JetStream.
// It maps the domain Event.Type to the NATS subject and translates JSON headers
// into NATS message headers.
// It automatically sets the "Nats-Msg-Id" header using the Event ID to enable
// JetStream's built-in idempotent publishing (message deduplication).
//
// If there is no open connection or the publish fails, it returns a
// relay.PublishError, classifying the failure as retryable based on
// NATS-specific error codes.
func (n *Publisher) Publish(ctx context.Context, event relay.Event) error {
	s := n.session.Load()
	if s == nil || s.conn.IsClosed() {
		// Retryable: the engine reconnects after its next failed health check.
		return &relay.PublishError{
			Err:         errNotConnected,
			IsRetryable: true,
			Code:        "NATS_NOT_CONNECTED",
		}
	}

	msg, err := newMsg(event)
	if err != nil {
		return err
	}

	pubCtx, cancel := context.WithTimeout(ctx, n.publishTimeout)
	defer cancel()

	_, err = s.js.PublishMsg(msg, nats.Context(pubCtx))

	if err != nil {
		return &relay.PublishError{
			Err:         fmt.Errorf("jetstream publish failed: %w", err),
			IsRetryable: isNatsErrorRetryable(err),
			Code:        "NATS_JS_PUBLISH_ERROR",
		}
	}

	return nil
}

const (
	msgIDHeader        = "Nats-Msg-Id"
	partitionKeyHeader = "X-Partition-Key"
)

// newMsg maps an event to a NATS message. The relay's own headers replace
// user headers of the same name, in any case: NATS header names are
// case-sensitive, so "nats-msg-id" would otherwise be sent next to
// "Nats-Msg-Id" instead of replacing it.
func newMsg(event relay.Event) (*nats.Msg, error) {
	userHeaders, err := event.HeaderMap()
	if err != nil {
		return nil, err
	}

	msg := nats.NewMsg(event.Type)
	msg.Data = event.Payload

	pkey := event.GetPartitionKey()
	for key, value := range userHeaders {
		if err := validateHeaderName(key); err != nil {
			return nil, relay.InvalidHeadersError(err)
		}
		if strings.EqualFold(key, msgIDHeader) ||
			(pkey != "" && strings.EqualFold(key, partitionKeyHeader)) {
			continue
		}
		msg.Header.Set(key, value)
	}

	msg.Header.Set(msgIDHeader, event.ID.String())
	if pkey != "" {
		msg.Header.Set(partitionKeyHeader, pkey)
	}
	return msg, nil
}

// validateHeaderName rejects names that would corrupt the header block,
// which is written as "Name: value" lines. Values need no check: the
// client replaces CR and LF in them with spaces.
func validateHeaderName(name string) error {
	if name == "" {
		return errors.New("header name is empty")
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c <= ' ' || c == ':' || c >= 0x7f {
			return fmt.Errorf(
				"header name %q is not valid for NATS: "+
					"it must be printable ASCII without spaces or colons", name,
			)
		}
	}
	return nil
}

// Close shuts down the NATS connection. Connect fails afterwards, so the
// publisher cannot be reopened.
func (n *Publisher) Close(_ context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.closed = true
	if s := n.session.Load(); s != nil {
		s.conn.Close()
	}
	return nil
}

// Ping reports whether the connection to the NATS server is up. It only
// reads the connection's state: reconnecting is left to the client and the
// engine, so health checks have no side effects.
func (n *Publisher) Ping(_ context.Context) error {
	s := n.session.Load()
	if s == nil {
		return errNotConnected
	}
	if status := s.conn.Status(); status != nats.CONNECTED {
		return fmt.Errorf("nats connection status is %s", status.String())
	}
	return nil
}

// isNatsErrorRetryable evaluates whether a NATS error should trigger a retry attempt.
// It considers network timeouts and connection issues as retryable, while marking
// permanent failures like authorization errors, invalid subjects, or payload
// limit violations as non-retryable.
func isNatsErrorRetryable(err error) bool {

	if err == nil {
		return false
	}

	if utils.IsContextErr(err) {
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
