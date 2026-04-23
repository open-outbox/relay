package publishers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/open-outbox/relay/internal/relay"
	"github.com/segmentio/kafka-go"
)

// KafkaConfig holds the configuration for the Kafka publisher.
// It maps directly to the settings used by the segmentio/kafka-go writer,
// allowing for fine-grained control over batching, timeouts, and durability.
//
// Note: In the context of this relay, BatchSize is typically set to 1
// to ensure the relay's internal batching logic remains the primary
// driver of delivery frequency.
type KafkaConfig struct {
	Brokers           []string
	MaxAttempts       int
	WriteTimeout      time.Duration
	ReadTimeout       time.Duration
	ConnectionTimeout time.Duration
	BatchSize         int
	BatchBytes        int64
	BatchTimeout      time.Duration
	Async             bool
	Compression       kafka.Compression
	RequiredAcks      kafka.RequiredAcks
}

// Kafka is a publisher that writes messages to an Apache Kafka cluster.
// It implements the relay.Publisher interface.
type Kafka struct {
	writer *kafka.Writer
	cfg    KafkaConfig
}

// NewKafka initializes a new Kafka writer with strict ordering and safety.
// It handles the parsing of broker URLs (stripping kafka:// prefixes)
// and configures the underlying writer with a Hash balancer to ensure
// messages with the same PartitionKey are always routed to the same
// Kafka partition.
func NewKafka(cfg KafkaConfig) (*Kafka, error) {

	if len(cfg.Brokers) < 1 || (len(cfg.Brokers) == 1 && cfg.Brokers[0] == "") {
		return nil, fmt.Errorf(
			"kafka connection failed: no broker addresses provided in configuration",
		)
	}
	return &Kafka{
		cfg: cfg,
	}, nil

}

// Connect satisfies the relay.Publisher interface.
// It initializes the Kafka writer using the stored configuration.
func (k *Kafka) Connect(ctx context.Context) error {

	if k.writer != nil {
		return nil
	}

	// Perform a quick Dial check to ensure brokers are reachable
	// before committing to the writer lifecycle.
	if err := k.Ping(ctx); err != nil {
		return fmt.Errorf("initial kafka connection check failed: %w, %v", err, k.cfg.Brokers)
	}

	k.writer = &kafka.Writer{
		Addr:         kafka.TCP(k.cfg.Brokers...),
		Balancer:     &kafka.Hash{},
		RequiredAcks: k.cfg.RequiredAcks,
		Async:        k.cfg.Async,

		MaxAttempts:  k.cfg.MaxAttempts,
		WriteTimeout: k.cfg.WriteTimeout,
		ReadTimeout:  k.cfg.ReadTimeout,

		// Critical Performance Overrides
		BatchSize:    k.cfg.BatchSize,
		BatchBytes:   k.cfg.BatchBytes,
		BatchTimeout: k.cfg.BatchTimeout,
		Compression:  k.cfg.Compression,
	}

	return nil

}

// Publish sends a single event to Kafka.
// It maps the domain event to a Kafka message, using the Event.Type as the topic.
// If the operation fails, it wraps the error in a relay.PublishError,
// classifying it as retryable based on the Kafka error code.
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

// PublishBatch writes a slice of events to Kafka in a single transaction/request.
// This is highly efficient for high-volume relays. If any individual message
// mapping fails (e.g., malformed headers), the entire batch operation returns
// an error immediately. The segmentio driver handles the actual transport
// level batching and acknowledgment.
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

// mapToKafkaMessage transforms a generic relay.Event into a kafka.Message.
// It handles JSON unmarshaling of headers, sets the message key from the
// PartitionKey, and injects the X-Event-ID header to allow for
// downstream deduplication.
func (k *Kafka) mapToKafkaMessage(event relay.Event) (kafka.Message, error) {
	var kafkaKey []byte

	pKey := event.GetPartitionKey()

	if pKey != "" {
		kafkaKey = []byte(pKey)
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

// Close gracefully shuts down the Kafka publisher.
// It blocks until all buffered messages are flushed or the context expires.
func (k *Kafka) Close(_ context.Context) error {
	if k == nil || k.writer == nil {
		return nil // Safe to close if never connected
	}

	// k.writer.Close() returns an error if the flush fails or if
	// the underlying connections cannot be closed cleanly.
	if err := k.writer.Close(); err != nil {
		return fmt.Errorf("failed to close kafka writer: %w", err)
	}

	return nil
}

// Ping verifies the connectivity to the Kafka brokers by attempting to
// fetch metadata or checking the underlying connection state.
func (k *Kafka) Ping(ctx context.Context) error {
	var addr string
	if k.writer != nil {
		addr = k.writer.Addr.String()
	} else {
		addr = k.cfg.Brokers[0]
	}

	dialer := &kafka.Dialer{
		Timeout:   k.cfg.ConnectionTimeout,
		DualStack: true,
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to dial kafka broker at %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	return nil
}

// isKafkaErrorRetryable classifies Kafka-specific errors to determine
// if the relay should attempt to republish the message.
// It considers network timeouts, connection issues, and temporary
// broker-side states as retryable.
func isKafkaErrorRetryable(err error) bool {
	if err == nil {
		return true
	}

	if isContextError(err) {
		return true
	}

	var writeErrs kafka.WriteErrors
	if errors.As(err, &writeErrs) {
		for _, e := range writeErrs {
			if e != nil {
				if !isIndividualKafkaErrorRetryable(e) {
					return false
				}
			}
		}
		return true
	}

	return isIndividualKafkaErrorRetryable(err)
}

func isIndividualKafkaErrorRetryable(err error) bool {
	var kErr kafka.Error
	if !errors.As(err, &kErr) {
		return true
	}

	switch kErr {
	case
		kafka.InvalidMessage,
		kafka.UnknownTopicOrPartition,
		kafka.InvalidMessageSize,
		kafka.MessageSizeTooLarge,
		kafka.InvalidTopic:
		return false
	default:
		return true
	}
}
