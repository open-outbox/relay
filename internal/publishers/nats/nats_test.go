package nats

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMsg_Headers(t *testing.T) {
	id := uuid.New()
	pkey := "order-1"

	msg, err := newMsg(relay.Event{
		ID:           id,
		Type:         "orders.v1",
		PartitionKey: &pkey,
		Payload:      []byte(`{}`),
		Headers: json.RawMessage(`{"tenant":"acme","retry":3,"skip":null,` +
			`"nats-msg-id":"spoofed","x-partition-key":"spoofed"}`),
	})
	require.NoError(t, err)

	// User headers with a relay header's name, in any case, are replaced.
	assert.Equal(t, nats.Header{
		"tenant":          {"acme"},
		"retry":           {"3"},
		"Nats-Msg-Id":     {id.String()},
		"X-Partition-Key": {pkey},
	}, msg.Header)
	assert.Equal(t, "orders.v1", msg.Subject)
}

func TestNewMsg_UserPartitionKeyHeaderKeptWithoutPartitionKey(t *testing.T) {
	msg, err := newMsg(relay.Event{
		Type:    "orders.v1",
		Headers: json.RawMessage(`{"X-Partition-Key":"from-user"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "from-user", msg.Header.Get("X-Partition-Key"))
}

func TestNewMsg_InvalidHeaders(t *testing.T) {
	tests := map[string]string{
		"empty name":      `{"":"x"}`,
		"space in name":   `{"my header":"x"}`,
		"colon in name":   `{"a:b":"x"}`,
		"newline in name": `{"a\nInjected":"x"}`,
		"non-ASCII name":  `{"café":"x"}`,
		"nested value":    `{"user":{"id":1}}`,
	}

	for name, headers := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newMsg(relay.Event{Type: "orders.v1", Headers: json.RawMessage(headers)})

			var pubErr *relay.PublishError
			require.ErrorAs(t, err, &pubErr)
			assert.False(t, pubErr.IsRetryable)
			assert.Equal(t, "INVALID_HEADERS", pubErr.Code)
		})
	}
}

// Before Connect there is no session: Publish must return a retryable
// error instead of dereferencing a nil connection, and Ping must fail.
func TestPublisher_BeforeConnect(t *testing.T) {
	p, err := New("nats://127.0.0.1:1", time.Second, time.Second)
	require.NoError(t, err)

	err = p.Publish(context.Background(), relay.Event{ID: uuid.New(), Type: "orders.v1"})
	var pErr *relay.PublishError
	require.ErrorAs(t, err, &pErr)
	assert.True(t, pErr.IsRetryable)
	assert.Equal(t, "NATS_NOT_CONNECTED", pErr.Code)

	assert.ErrorIs(t, p.Ping(context.Background()), errNotConnected)
	assert.NoError(t, p.Close(context.Background()), "Close without a connection")
}

// After Close the engine's health loop must not reopen the connection.
func TestPublisher_ConnectAfterClose(t *testing.T) {
	p, err := New("nats://127.0.0.1:1", time.Second, time.Second)
	require.NoError(t, err)
	require.NoError(t, p.Close(context.Background()))
	assert.ErrorIs(t, p.Connect(context.Background()), errPublisherClosed)
}
