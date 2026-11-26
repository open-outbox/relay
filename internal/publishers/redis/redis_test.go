package redis

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamValues(t *testing.T) {
	id := uuid.New()
	pkey := "order-1"

	values, err := streamValues(relay.Event{
		ID:           id,
		Type:         "orders.v1",
		PartitionKey: &pkey,
		Payload:      []byte(`{"a":1}`),
		// "id" cannot collide with the relay's field: headers are prefixed.
		Headers: json.RawMessage(`{"tenant":"acme","retry":3,"skip":null,"id":"user"}`),
	})
	require.NoError(t, err)

	assert.Equal(t, map[string]any{
		"id":            id.String(),
		"payload":       []byte(`{"a":1}`),
		"partition_key": pkey,
		"h:tenant":      "acme",
		"h:retry":       "3",
		"h:id":          "user",
	}, values)
}

func TestStreamValues_NestedHeadersArePermanent(t *testing.T) {
	_, err := streamValues(relay.Event{
		Type:    "orders.v1",
		Headers: json.RawMessage(`{"tags":["a"]}`),
	})

	var pubErr *relay.PublishError
	require.ErrorAs(t, err, &pubErr)
	assert.False(t, pubErr.IsRetryable)
	assert.Equal(t, "INVALID_HEADERS", pubErr.Code)
}
