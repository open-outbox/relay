package publishers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
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
	Balancer          kafka.Balancer
	TLSEnabled        bool
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
	writer     *kafka.Writer
	cfg        KafkaConfig
	dialer     *kafka.Dialer
	transport  *kafka.Transport
	pingCursor atomic.Uint32
}

// defaultPingTimeout is used when the read timeout is zero.
const defaultPingTimeout = 10 * time.Second

// NewKafka builds the Kafka writer from cfg. It expects cfg.Brokers to hold
// plain host:port addresses (URL parsing happens in the container), builds a
// shared TLS/SASL transport for producing and pinging, and routes messages
// with cfg.Balancer (kafka.Hash when nil) so messages with the same
// PartitionKey always land on the same partition. No connection is opened
// here; call Connect to verify reachability.
func NewKafka(cfg KafkaConfig) (*Kafka, error) {

	if len(cfg.Brokers) < 1 || (len(cfg.Brokers) == 1 && cfg.Brokers[0] == "") {
		return nil, fmt.Errorf(
			"kafka connection failed: no broker addresses provided in configuration",
		)
	}

	cfg.SASLMechanism = strings.ToLower(strings.TrimSpace(cfg.SASLMechanism))

	k := &Kafka{cfg: cfg}

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
// NewKafka and connects lazily, so Connect only verifies that at least one
// broker is reachable (including the TLS and SASL handshakes).
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
// It blocks until all buffered messages are flushed or the context expires,
// whichever comes first. If the context expires, Close returns its error and
// any messages still buffered (async mode) may be lost; the flush keeps
// running in the background until the writer's own timeouts end it.
func (k *Kafka) Close(ctx context.Context) error {
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
func (k *Kafka) Ping(ctx context.Context) error {
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

func (k *Kafka) fetchBrokers(ctx context.Context, addr string) ([]kafka.Broker, error) {
	// Not k.transport: it answers metadata requests from its cache.
	conn, err := k.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(k.pingDeadline(ctx)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	brokers, err := conn.Brokers()
	if err != nil {
		return nil, fmt.Errorf("metadata request failed: %w", err)
	}
	if len(brokers) == 0 {
		return nil, errors.New("metadata response listed no brokers")
	}
	return brokers, nil
}

func (k *Kafka) pingAdvertised(
	ctx context.Context,
	bootstrap string,
	brokers []kafka.Broker,
) error {
	start := int(k.pingCursor.Add(1)) % len(brokers)
	errs := make([]error, 0, len(brokers))

	for i := range brokers {
		b := brokers[(start+i)%len(brokers)]
		addr := net.JoinHostPort(b.Host, strconv.Itoa(b.Port))

		conn, err := k.dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}

		errs = append(errs, fmt.Errorf("broker %d at %s: %w", b.ID, addr, err))
		if ctx.Err() != nil {
			break
		}
	}

	return fmt.Errorf(
		"bootstrap broker %s answered, but none of the brokers it advertises is reachable "+
			"(check the brokers' advertised.listeners): %w",
		bootstrap, errors.Join(errs...),
	)
}

func (k *Kafka) pingDeadline(ctx context.Context) time.Time {
	timeout := k.cfg.ReadTimeout
	if timeout <= 0 {
		timeout = defaultPingTimeout
	}
	deadline := time.Now().Add(timeout)

	// kafka.Conn ignores ctx, so the caller's deadline only applies through
	// this one; keep whichever comes first.
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		return d
	}
	return deadline
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

	if utils.IsContextErr(err) {
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

	// Only errors caused by the message itself are permanent: retrying the
	// same event can never succeed. Everything else (leader elections,
	// metadata propagation, auth or ACL misconfiguration, broker outages)
	// can resolve without the event changing, so it stays retryable.
	//
	// Note that UnknownTopicOrPartition and InvalidMessage (CORRUPT_MESSAGE)
	// are deliberately absent: Kafka marks both as retriable, since they
	// occur transiently during leader moves, topic auto-creation and
	// in-transit corruption.
	switch kErr {
	case
		kafka.InvalidMessageSize,
		kafka.MessageSizeTooLarge,
		kafka.RecordListTooLarge,
		kafka.InvalidRecord,
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

	// Supplying any TLS material, or asking to skip verification, implies TLS.
	// TLSEnabled covers brokers with publicly trusted certificates, where none
	// of those are needed: RootCAs stays nil and the system trust store is used.
	tlsEnabled := k.cfg.TLSEnabled || ca != nil || cert != nil || key != nil || k.cfg.Insecure

	var tlsConfig *tls.Config
	if tlsEnabled {
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

	// The Transport is what the Writer uses to produce messages, so it needs
	// its own dial, TLS and SASL settings; it does not inherit them from the
	// Dialer above (which is only used by Ping).
	netDialer := &net.Dialer{
		Timeout:   k.cfg.ConnectionTimeout,
		KeepAlive: k.cfg.KeepAlive,
	}

	transport := &kafka.Transport{
		Dial:        netDialer.DialContext,
		DialTimeout: k.cfg.ConnectionTimeout,
		TLS:         tlsConfig,
		SASL:        dialer.SASLMechanism,
		IdleTimeout: k.cfg.IdleTimeout,
	}

	return dialer, transport, nil
}
