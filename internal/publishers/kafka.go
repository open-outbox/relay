package publishers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/utils"
	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"github.com/segmentio/kafka-go/sasl/plain"
	"github.com/segmentio/kafka-go/sasl/scram"
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
	TLSCA             string
	TLSCert           string
	TLSKey            string
	TLSVersion        uint16
	ServerName        string
	Insecure          bool
	SASLMechanism     string
	Username          string
	Password          string
	IdleTimeout       time.Duration
	KeepAlive         time.Duration
}

// Kafka is a publisher that writes messages to an Apache Kafka cluster.
// It implements the relay.Publisher interface.
type Kafka struct {
	writer    *kafka.Writer
	cfg       KafkaConfig
	dialer    *kafka.Dialer
	transport *kafka.Transport
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

	k := &Kafka{cfg: cfg}

	dialer, transport, err := k.buildTransport()
	if err != nil {
		return nil, fmt.Errorf("failed to compile transport configuration: %w", err)
	}
	k.dialer = dialer
	k.transport = transport

	k.writer = &kafka.Writer{
		Addr:         kafka.TCP(k.cfg.Brokers...),
		Balancer:     &kafka.Hash{},
		RequiredAcks: cfg.RequiredAcks,
		Async:        cfg.Async,
		MaxAttempts:  cfg.MaxAttempts,
		WriteTimeout: cfg.WriteTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		BatchSize:    cfg.BatchSize,
		BatchBytes:   cfg.BatchBytes,
		Transport:    k.transport,
	}

	return k, nil

}

// Connect satisfies the relay.Publisher interface.
// It initializes the Kafka writer using the stored configuration.
func (k *Kafka) Connect(ctx context.Context) error {
	if err := k.Ping(ctx); err != nil {
		return fmt.Errorf("failed to establish broker connectivity: %w", err)
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

	conn, err := k.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to dial kafka broker at %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	return nil
}

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

func (k *Kafka) getSASLMechanism() (sasl.Mechanism, error) {
	switch k.cfg.SASLMechanism {
	case "plain":
		return plain.Mechanism{
			Username: k.cfg.Username,
			Password: k.cfg.Password,
		}, nil

	case "scram-sha-256":
		mechanism, err := scram.Mechanism(scram.SHA256, k.cfg.Username, k.cfg.Password)
		if err != nil {
			return nil, fmt.Errorf("failed to create SCRAM-SHA-256 mechanism: %w", err)
		}
		return mechanism, nil

	case "scram-sha-512":
		mechanism, err := scram.Mechanism(scram.SHA512, k.cfg.Username, k.cfg.Password)
		if err != nil {
			return nil, fmt.Errorf("failed to create SCRAM-SHA-512 mechanism: %w", err)
		}
		return mechanism, nil

	default:
		return nil, fmt.Errorf("unsupported SASL mechanism for Kafka: %s", k.cfg.SASLMechanism)
	}
}

func (k *Kafka) buildTransport() (*kafka.Dialer, *kafka.Transport, error) {

	ca, err := utils.LoadBytes(k.cfg.TLSCA)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load TLS CA: %w", err)
	}
	cert, err := utils.LoadBytes(k.cfg.TLSCert)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load TLS Cert: %w", err)
	}
	key, err := utils.LoadBytes(k.cfg.TLSKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load TLS Key: %w", err)
	}

	var tlsConfig *tls.Config
	if ca != nil || cert != nil || key != nil || k.cfg.Insecure {
		tlsConfig = &tls.Config{
			ServerName:         k.cfg.ServerName,
			InsecureSkipVerify: k.cfg.Insecure,
			MinVersion:         k.cfg.TLSVersion,
		}

		if ca != nil {
			cp := x509.NewCertPool()
			if !cp.AppendCertsFromPEM(ca) {
				return nil, nil, fmt.Errorf(
					"failed to append CA certificate to pool: invalid or malformed X509 PEM data",
				)
			}
			tlsConfig.RootCAs = cp
		}

		if cert != nil || key != nil {
			if cert == nil || key == nil {
				return nil, nil, fmt.Errorf(
					"incomplete mTLS configuration: both TLSCert and TLSKey must be provided together",
				)
			}

			pair, err := tls.X509KeyPair(cert, key)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to parse mTLS keypair: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{pair}
		}
	}

	dialer := &kafka.Dialer{
		Timeout:   k.cfg.ConnectionTimeout,
		DualStack: true,
		TLS:       tlsConfig,
		KeepAlive: k.cfg.KeepAlive,
	}

	if k.cfg.SASLMechanism != "" {
		mechanism, err := k.getSASLMechanism()
		if err != nil {
			return nil, nil, err
		}
		dialer.SASLMechanism = mechanism
	}

	transport := &kafka.Transport{
		Dial:        dialer.DialFunc,
		TLS:         tlsConfig,
		IdleTimeout: k.cfg.IdleTimeout,
	}

	return dialer, transport, nil
}
