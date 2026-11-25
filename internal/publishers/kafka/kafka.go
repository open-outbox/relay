// Package kafka publishes relay events to Apache Kafka topics.
package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/open-outbox/relay/internal/relay"
	"github.com/segmentio/kafka-go"
)

// Publisher is a publisher that writes messages to an Apache Kafka cluster.
// It implements the relay.Publisher interface.
type Publisher struct {
	writer     *kafka.Writer
	cfg        Config
	dialer     *kafka.Dialer
	transport  *kafka.Transport
	pingCursor atomic.Uint32
}

// New builds the Kafka writer from cfg. It expects cfg.Brokers to hold
// plain host:port addresses (URL parsing happens in the container), builds a
// shared TLS/SASL transport for producing and pinging, and routes messages
// with cfg.Balancer (kafka.Hash when nil) so messages with the same
// PartitionKey always land on the same partition. No connection is opened
// here; call Connect to verify reachability.
func New(cfg Config) (*Publisher, error) {

	if len(cfg.Brokers) < 1 || (len(cfg.Brokers) == 1 && cfg.Brokers[0] == "") {
		return nil, fmt.Errorf(
			"kafka connection failed: no broker addresses provided in configuration",
		)
	}

	cfg.SASLMechanism = strings.ToLower(strings.TrimSpace(cfg.SASLMechanism))

	k := &Publisher{cfg: cfg}

	dialer, transport, err := k.buildTransport()
	if err != nil {
		return nil, fmt.Errorf("failed to compile transport configuration: %w", err)
	}
	k.dialer = dialer
	k.transport = transport

	balancer := cfg.Balancer
	if balancer == nil {
		balancer = &kafka.Hash{}
	}

	k.writer = &kafka.Writer{
		Addr:         kafka.TCP(k.cfg.Brokers...),
		Balancer:     balancer,
		RequiredAcks: cfg.RequiredAcks,
		Async:        cfg.Async,
		MaxAttempts:  cfg.MaxAttempts,
		WriteTimeout: cfg.WriteTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		BatchSize:    cfg.BatchSize,
		BatchBytes:   cfg.BatchBytes,
		BatchTimeout: cfg.BatchTimeout,
		Compression:  cfg.Compression,
		Transport:    k.transport,
	}

	return k, nil

}

// Connect satisfies the relay.Publisher interface. The writer is created by
// New and connects lazily, so Connect only verifies that at least one
// broker is reachable (including the TLS and SASL handshakes).
func (k *Publisher) Connect(ctx context.Context) error {
	if err := k.Ping(ctx); err != nil {
		return fmt.Errorf("failed to establish broker connectivity: %w", err)
	}
	return nil
}

// Ping verifies that the cluster can actually be produced to.
// It runs in two steps:
//
//  1. Dial a bootstrap broker (TCP, TLS and SASL handshakes) and fetch the
//     cluster's broker list with a metadata request. This goes over the
//     network on every call; the producer's Transport is deliberately not
//     used, because it answers metadata requests from its cache.
//  2. Dial one of the brokers the cluster advertises. Producing connects to
//     these advertised addresses (advertised.listeners), not the bootstrap
//     ones, so a misconfigured listener would otherwise pass Ping while
//     every publish fails.
//
// Ping succeeds as soon as one bootstrap broker answers and one advertised
// broker is reachable.
func (k *Publisher) Ping(ctx context.Context) error {
	errs := make([]error, 0, len(k.cfg.Brokers))

	for _, addr := range k.cfg.Brokers {
		brokers, err := k.fetchBrokers(ctx, addr)
		if err == nil {
			return k.pingAdvertised(ctx, addr, brokers)
		}

		errs = append(errs, fmt.Errorf("broker %s: %w", addr, err))
		if ctx.Err() != nil {
			break
		}
	}

	return fmt.Errorf("failed to reach any kafka broker: %w", errors.Join(errs...))
}

// Publish sends a single event to Kafka.
// It maps the domain event to a Kafka message, using the Event.Type as the topic.
// If the operation fails, it wraps the error in a relay.PublishError,
// classifying it as retryable based on the Kafka error code.
func (k *Publisher) Publish(ctx context.Context, event relay.Event) error {
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
func (k *Publisher) PublishBatch(ctx context.Context, events []relay.Event) error {
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

// Close gracefully shuts down the Kafka publisher.
// It blocks until all buffered messages are flushed or the context expires,
// whichever comes first. If the context expires, Close returns its error and
// any messages still buffered (async mode) may be lost; the flush keeps
// running in the background until the writer's own timeouts end it.
func (k *Publisher) Close(ctx context.Context) error {
	if k == nil || k.writer == nil {
		return nil // Safe to close if never connected
	}

	return closeWithContext(ctx, func() error {
		err := k.writer.Close()
		// The writer only closes transports it created itself; ours is
		// passed in explicitly, so release its pooled connections here.
		if k.transport != nil {
			k.transport.CloseIdleConnections()
		}
		return err
	})
}

// closeWithContext runs closeFn and waits for it, giving up when ctx is done.
// closeFn keeps running in the background after a timeout; the result
// channel is buffered so that goroutine can always finish and exit.
func closeWithContext(ctx context.Context, closeFn func() error) error {
	done := make(chan error, 1)
	go func() { done <- closeFn() }()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("failed to close kafka writer: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf(
			"kafka writer close did not finish before shutdown deadline: %w",
			ctx.Err(),
		)
	}
}

func (k *Publisher) mapToKafkaMessage(event relay.Event) (kafka.Message, error) {
	if event.Type == "" {
		return kafka.Message{}, &relay.PublishError{
			Err:         errors.New("event type is empty, so there is no topic to publish to"),
			IsRetryable: false,
			Code:        "INVALID_TOPIC",
		}
	}

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
