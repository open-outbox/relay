package publishers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/open-outbox/relay/internal/relay"
	"github.com/segmentio/kafka-go"
)

// Kafka implements the relay.Publisher interface using the segmentio/kafka-go client.
type Kafka struct {
	writer *kafka.Writer
}

// NewKafka initializes a new Kafka writer with strict ordering and safety.
func NewKafka(brokers string) *Kafka {
	brokerList := strings.Split(strings.TrimPrefix(brokers, "kafka://"), ",")

	return &Kafka{
		writer: &kafka.Writer{
			Addr: kafka.TCP(brokerList...),

			// USE THE HASH BALANCER
			// Ensures events with the same Key land in the same partition.
			Balancer: &kafka.Hash{},

			// AT-LEAST-ONCE SAFETY
			// Require all in-sync replicas to ack the message.
			RequiredAcks: kafka.RequireAll,

			// RETRY LOGIC
			MaxAttempts: 5,
			Async:       false,
		},
	}
}

// Publish writes the event to Kafka using the PartitionKey for ordering.
func (k *Kafka) Publish(ctx context.Context, event relay.Event) error {
	msg, err := k.mapToKafkaMessage(event)
	if err != nil {
		return err
	}

	if err := k.writer.WriteMessages(ctx, msg); err != nil {
		return &relay.PublishError{
			Err:         fmt.Errorf("kafka write failed: %w", err),
			IsRetryable: isKafkaErrorRetryable(err),
			Code:        "KAFKA_WRITE_ERROR",
		}
	}
	return nil
}

// PublishBatch writes multiple events in a single Kafka request.
func (k *Kafka) PublishBatch(ctx context.Context, events []relay.Event) error {
	if len(events) == 0 {
		return nil
	}

	msgs := make([]kafka.Message, 0, len(events))
	for _, event := range events {
		msg, err := k.mapToKafkaMessage(event)
		if err != nil {
			return err // Returns immediately if an event is malformed (Headers unmarshal fails)
		}
		msgs = append(msgs, msg)
	}

	// segmentio/kafka-go handles the batching/distribution internally
	err := k.writer.WriteMessages(ctx, msgs...)
	if err != nil {
		return &relay.PublishError{
			Err:         fmt.Errorf("kafka batch write failed: %w", err),
			IsRetryable: isKafkaErrorRetryable(err),
			Code:        "KAFKA_BATCH_WRITE_ERROR",
		}
	}

	return nil
}

// mapToKafkaMessage is a helper to convert our domain Event to a Kafka message.
func (k *Kafka) mapToKafkaMessage(event relay.Event) (kafka.Message, error) {
	var kafkaKey []byte
	if event.PartitionKey != "" {
		kafkaKey = []byte(event.PartitionKey)
	}

	var userHeaders map[string]string
	if len(event.Headers) > 0 {
		if err := json.Unmarshal(event.Headers, &userHeaders); err != nil {
			return kafka.Message{}, &relay.PublishError{
				Err:         fmt.Errorf("failed to unmarshal event headers: %w", err),
				IsRetryable: false,
				Code:        "INVALID_HEADERS",
			}
		}
	}

	headers := make([]kafka.Header, 0, len(userHeaders)+1)
	for key, value := range userHeaders {
		headers = append(headers, kafka.Header{Key: key, Value: []byte(value)})
	}

	headers = append(headers, kafka.Header{
		Key:   "X-Event-ID",
		Value: []byte(event.ID.String()),
	})

	return kafka.Message{
		Key:     kafkaKey,
		Topic:   event.Type,
		Value:   event.Payload,
		Headers: headers,
	}, nil
}

// Close gracefully shuts down the Kafka writer.
// It ensures all buffered messages are flushed to the brokers before closing.
func (k *Kafka) Close() error {
	if k.writer == nil {
		return nil
	}

	// k.writer.Close() returns an error if the flush fails or if
	// the underlying connections cannot be closed cleanly.
	if err := k.writer.Close(); err != nil {
		return fmt.Errorf("failed to close kafka writer: %w", err)
	}

	return nil
}

// TODO: Sanity check
func isKafkaErrorRetryable(err error) bool {
	if err == nil {
		return false
	}

	if isContextError(err) {
		return true
	}

	var writeErrs kafka.WriteErrors
	if errors.As(err, &writeErrs) {
		for _, e := range writeErrs {
			if e != nil {
				return isKafkaErrorRetryable(e)
			}
		}
		return false
	}

	var tempErr interface{ Temporary() bool }
	if errors.As(err, &tempErr) {
		return tempErr.Temporary()
	}

	return false
}
