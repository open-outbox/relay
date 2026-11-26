package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/open-outbox/relay/internal/relay"
	"github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKafka_Close(t *testing.T) {
	t.Run("is safe on a nil or unbuilt publisher", func(t *testing.T) {
		var k *Publisher
		assert.NoError(t, k.Close(context.Background()))
		assert.NoError(t, (&Publisher{}).Close(context.Background()))
	})

	t.Run("returns promptly when nothing is buffered", func(t *testing.T) {
		k, err := New(Config{Brokers: []string{freeClosedAddr(t)}})
		require.NoError(t, err)
		assert.NoError(t, k.Close(context.Background()))
	})
}

func TestCloseWithContext(t *testing.T) {
	t.Run("returns the close result when it finishes in time", func(t *testing.T) {
		assert.NoError(t, closeWithContext(context.Background(), func() error { return nil }))

		boom := errors.New("boom")
		err := closeWithContext(context.Background(), func() error { return boom })
		assert.ErrorIs(t, err, boom)
	})

	t.Run("gives up when the context expires and lets close finish later", func(t *testing.T) {
		release := make(chan struct{})
		finished := make(chan struct{})

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		err := closeWithContext(ctx, func() error {
			<-release // simulate a flush stuck on an unresponsive broker
			close(finished)
			return nil
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)

		// The background close must still be able to complete (no leak).
		close(release)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("background close never completed")
		}
	})
}

func TestNew_Balancer(t *testing.T) {
	t.Run("defaults to FNV-1a hash when unset", func(t *testing.T) {
		k, err := New(Config{Brokers: []string{"localhost:9092"}})
		require.NoError(t, err)
		assert.IsType(t, &kafka.Hash{}, k.writer.Balancer)
	})

	t.Run("uses the configured balancer", func(t *testing.T) {
		k, err := New(Config{
			Brokers:  []string{"localhost:9092"},
			Balancer: kafka.Murmur2Balancer{},
		})
		require.NoError(t, err)
		assert.IsType(t, kafka.Murmur2Balancer{}, k.writer.Balancer)
	})
}

func TestKafka_Publish_EmptyTypeIsPermanent(t *testing.T) {
	// The address is never dialled: the event is rejected before any write.
	k, err := New(Config{Brokers: []string{"127.0.0.1:1"}})
	require.NoError(t, err)

	calls := map[string]func() error{
		"Publish": func() error {
			return k.Publish(context.Background(), relay.Event{Type: ""})
		},
		"PublishBatch": func() error {
			return k.PublishBatch(context.Background(), []relay.Event{{Type: ""}})
		},
	}

	for name, publish := range calls {
		t.Run(name, func(t *testing.T) {
			var pubErr *relay.PublishError
			require.ErrorAs(t, publish(), &pubErr)
			assert.False(t, pubErr.IsRetryable)
			assert.Equal(t, "INVALID_TOPIC", pubErr.Code)
		})
	}
}

func TestNew_MetadataScope(t *testing.T) {
	k, err := New(Config{
		Brokers:        []string{"127.0.0.1:1"},
		MetadataTopics: []string{"orders.v1", "payments.v1"},
		MetadataTTL:    30 * time.Second,
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"orders.v1", "payments.v1"}, k.transport.MetadataTopics)
	assert.Equal(t, 30*time.Second, k.transport.MetadataTTL)

	t.Run("listed topic is mapped", func(t *testing.T) {
		msg, err := k.mapToKafkaMessage(relay.Event{Type: "orders.v1"})
		require.NoError(t, err)
		assert.Equal(t, "orders.v1", msg.Topic)
	})

	// The address is never dialled: the event is rejected before any write.
	calls := map[string]func() error{
		"Publish": func() error {
			return k.Publish(context.Background(), relay.Event{Type: "other.v1"})
		},
		"PublishBatch": func() error {
			return k.PublishBatch(context.Background(), []relay.Event{{Type: "other.v1"}})
		},
	}
	for name, publish := range calls {
		t.Run(name+" rejects unlisted topic", func(t *testing.T) {
			var pubErr *relay.PublishError
			require.ErrorAs(t, publish(), &pubErr)
			assert.True(t, pubErr.IsRetryable)
			assert.Equal(t, "TOPIC_NOT_IN_METADATA", pubErr.Code)
			assert.Contains(t, pubErr.Error(), `"other.v1"`)
		})
	}
}

func TestNew_MetadataScopeDefaultsToAllTopics(t *testing.T) {
	k, err := New(Config{Brokers: []string{"127.0.0.1:1"}})
	require.NoError(t, err)

	assert.Empty(t, k.transport.MetadataTopics)
	_, err = k.mapToKafkaMessage(relay.Event{Type: "any.topic"})
	require.NoError(t, err)
}
