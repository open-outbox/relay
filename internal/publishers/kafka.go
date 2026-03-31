package publishers

import (
	"context"
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

// NewKafka initializes a new Kafka writer.
// brokers should be in the format "host1:9092,host2:9092" or "kafka://host1:9092".
func NewKafka(brokers string) *Kafka {
	brokerList := strings.Split(strings.TrimPrefix(brokers, "kafka://"), ",")

	return &Kafka{
		writer: &kafka.Writer{
			Addr:     kafka.TCP(brokerList...),
			Balancer: &kafka.LeastBytes{},
			//RequiredAcks: kafka.RequireAll,
			//Async:        false,
		},
	}
}

// Publish satisfies the relay.Publisher interface.
// It writes the event to Kafka and waits for a broker confirmation if configured.
func (k *Kafka) Publish(ctx context.Context, event relay.Event) (relay.PublishResult, error) {
	msg := kafka.Message{
		Key:   []byte(event.ID.String()),
		Topic: event.Type,
		Value: event.Payload,
	}

	err := k.writer.WriteMessages(ctx, msg)
	if err != nil {
		return relay.PublishResult{}, &relay.PublishError{
			Err:         fmt.Errorf("kafka publish failed: %w", err),
			IsRetryable: isKafkaErrorRetryable(err),
			Code:        "KAFKA_WRITE_ERROR",
		}
	}

	return relay.PublishResult{
		Status:     relay.StatusSuccess,
		ProviderID: event.ID.String(),
	}, nil
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

// Close gracefully shuts down the Kafka writer.
func (k *Kafka) Close() error {
	return k.writer.Close()
}
