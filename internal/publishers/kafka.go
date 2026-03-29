package publishers

import (
	"context"
	"strings"

	"github.com/open-outbox/relay/internal/relay"
	"github.com/segmentio/kafka-go"
)

type Kafka struct {
	writer *kafka.Writer
}

func NewKafka(brokers string) *Kafka {
	// We parse the brokers (e.g. "localhost:9092,localhost:9093")
	brokerList := strings.Split(strings.TrimPrefix(brokers, "kafka://"), ",")

	return &Kafka{
		writer: &kafka.Writer{
			Addr:     kafka.TCP(brokerList...),
			Balancer: &kafka.LeastBytes{},
		},
	}
}

func (k *Kafka) Publish(ctx context.Context, event relay.Event) error {
	return k.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(event.ID.String()), // Use ID as partition key
		Topic: event.Type,
		Value: event.Payload,
	})
}

func (k *Kafka) Close() error {
	return k.writer.Close()
}
