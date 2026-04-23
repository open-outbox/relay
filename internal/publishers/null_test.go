package publishers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/stretchr/testify/assert"
)

func TestNullPublisher(t *testing.T) {
	pub := NewNull() // Assumes this returns a *Stdout
	ctx := context.Background()

	event := relay.Event{
		ID:      uuid.New(),
		Payload: []byte(`{"message": "hello"}`),
		Type:    "test.event",
	}

	t.Run("Publish Single", func(t *testing.T) {
		err := pub.Publish(ctx, event)
		assert.NoError(t, err)
	})

	t.Run("Lifecycle", func(t *testing.T) {
		assert.NoError(t, pub.Connect(ctx))
		assert.NoError(t, pub.Ping(ctx))
		assert.NoError(t, pub.Close(ctx))
	})
}
