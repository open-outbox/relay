package kafka

import (
	"time"

	"github.com/segmentio/kafka-go"
)

// Config holds the configuration for the Kafka publisher.
// It maps directly to the settings used by the segmentio/kafka-go writer,
// allowing for fine-grained control over batching, timeouts, and durability.
//
// Note: In the context of this relay, BatchSize is typically set to 1
// to ensure the relay's internal batching logic remains the primary
// driver of delivery frequency.
type Config struct {
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
	OAuthTokenURL     string
	OAuthClientID     string
	OAuthClientSecret string
	OAuthScopes       []string
	OAuthExtensions   map[string]string
	IdleTimeout       time.Duration
	KeepAlive         time.Duration
}
