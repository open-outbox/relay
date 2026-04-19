// Package config handles the centralized configuration for the Outbox Relay.
// It follows a "Cloud-Native" priority hierarchy:
// 1. System Environment Variables (Highest Priority)
// 2. Local .env file overrides (Development)
// 3. Hardcoded Sanity Defaults (Fallback)
package config

import (
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/spf13/viper"
)

// Environment represents the operational mode of the relay.
type Environment string

const (
	// Development mode enables extra logging and relaxed security checks.
	Development Environment = "development"
	// Production mode optimizes for performance and strict error handling.
	Production Environment = "production"
)

// DefaultRetryJitter prevents "Thundering Herd" issues by staggering retries.
const DefaultRetryJitter = 0.15

// DefaultTableName is the standard table name for the events if none is provided.
const DefaultTableName = "openoutbox_events"

// Config represents the application's entire configuration state.
// It uses mapstructure tags for automatic type conversion from environment strings.
type Config struct {
	// StorageType determines the type of outbox storage (e.g., "postgres", "mysql").
	//	Default: "postgres"
	StorageType string `mapstructure:"STORAGE_TYPE"`

	// TableName is the name of the outbox table.
	// This allows users to avoid naming collisions in shared databases.
	//	Default: "openoutbox_events"
	StorageTableName string `mapstructure:"STORAGE_TABLE_NAME"`

	// PublisherType determines the message broker or output
	// (e.g., "nats", "kafka", "redis", "stdout").
	//	Default: "stdout"
	PublisherType string `mapstructure:"PUBLISHER_TYPE"`

	// StorageURL is the connection string for the outbox storage in common
	// database URL format (e.g., "postgres://user:pass@host:5432/db").
	StorageURL string `mapstructure:"STORAGE_URL"`

	// PublisherURL is the address or connection string for the message broker (e.g., "kafka:9092").
	PublisherURL string `mapstructure:"PUBLISHER_URL"`

	// PollInterval defines how frequently the engine checks the outbox table for new events.
	//	Default: "500ms"
	PollInterval time.Duration `mapstructure:"POLL_INTERVAL"`

	// BatchSize is the maximum number of events to process in a single iteration.
	//	Default: 100
	BatchSize int `mapstructure:"BATCH_SIZE"`

	// LeaseTimeout is the duration an event remains locked for processing before
	// being considered "stuck" and eligible for reaping.
	//	Default: "3m"
	LeaseTimeout time.Duration `mapstructure:"LEASE_TIMEOUT"`

	// ReapBatchSize is the number of stuck (expired lease) events to reset per cleanup cycle.
	//	Default: 100
	ReapBatchSize int `mapstructure:"REAP_BATCH_SIZE"`

	// ServerPort is the address/port for the HTTP health check and metrics server.
	//	Default: ":8080"
	ServerPort string `mapstructure:"SERVER_PORT"`

	// Environment specifies the mode (development/production) which affects logging and safety checks.
	// Default: "production"
	Environment Environment `mapstructure:"ENVIRONMENT"`

	// RELAY_ID is a unique identifier for this instance, used for
	// locking events in the database to prevent collisions.
	//	Default: os.Hostname()
	RelayID string `mapstructure:"RELAY_ID"`

	// RetryMaxAttempts is the maximum number of times an event will be
	// retried before being marked as DEAD.
	//	Default: 25
	RetryMaxAttempts int `mapstructure:"RETRY_MAX_ATTEMPTS"`

	// RetryBaseDelay is the starting delay for the exponential backoff strategy.
	//	Default: "1s"
	RetryBaseDelay time.Duration `mapstructure:"RETRY_BASE_DELAY"`

	// RetryMaxDelay is the upper limit for any single retry delay.
	//	Default: "24h"
	RetryMaxDelay time.Duration `mapstructure:"RETRY_MAX_DELAY"`

	// RetryJitter is the randomization factor (0.0 to 1.0) applied to retry delays to
	// prevent "Thundering Herd" issues.
	//	0.15
	RetryJitter float64 `mapstructure:"RETRY_JITTER"`

	// NatsPublishTimeout is the maximum time to wait for the NATS publisher to publish a message to
	// the NATs broker.
	//	Default: "5s"
	NatsPublishTimeout time.Duration `mapstructure:"NATS_PUBLISH_TIMEOUT"`

	// KafkaMaxAttempts is the number of write attempts...
	//	Default: 5
	KafkaMaxAttempts int `mapstructure:"KAFKA_MAX_ATTEMPTS"`

	// KafkaWriteTimeout is the deadline for writing a message batch to Kafka.
	//	Default: "10s"
	KafkaWriteTimeout time.Duration `mapstructure:"KAFKA_WRITE_TIMEOUT"`

	// KafkaReadTimeout is the deadline for reading a response (like ACKs) from the Kafka broker.
	//	Default: "10s"
	KafkaReadTimeout time.Duration `mapstructure:"KAFKA_READ_TIMEOUT"`

	// KafkaBatchSize defines how many messages the writer collects before flushing to the broker.
	//
	// In this Relay, setting this to 1 is critical to bypass the Kafka client's internal
	// buffering. Since the Relay Engine already batches events at the database level,
	// increasing this value will introduce unnecessary latency (double-batching).
	//
	//	Default: 1
	//	Note: Higher values may significantly decrease delivery speed in single-publish mode.
	KafkaBatchSize int `mapstructure:"KAFKA_BATCH_SIZE"`

	// KafkaBatchBytes is the maximum total size of a batch in bytes before the Kafka writer flushes.
	//	Default: 10485760 (10MB)
	KafkaBatchBytes int64 `mapstructure:"KAFKA_BATCH_BYTES"`

	// KafkaBatchTimeout is the maximum time to wait before flushing a partial batch to Kafka.
	//	Default: "10ms"
	KafkaBatchTimeout time.Duration `mapstructure:"KAFKA_BATCH_TIMEOUT"`

	// KafkaAsync enables non-blocking writes to the Kafka broker.
	//
	// In an Outbox Relay, this is typically set to false to ensure "At-Least-Once"
	// delivery. Enabling async mode increases throughput but risks data loss,
	// as the relay may mark events as delivered before the broker acknowledges them.
	//
	//	Default: false
	//
	//	Caution: Only enable if your system can tolerate potential message loss
	//	in exchange for maximum publishing throughput.
	KafkaAsync bool `mapstructure:"KAFKA_ASYNC"`

	// KafkaCompression specifies the compression algorithm used for
	// messages (none, gzip, snappy, lz4, zstd).
	// 	Default: "snappy"
	KafkaCompression string `mapstructure:"KAFKA_COMPRESSION"`

	// KafkaRequiredAcks defines the number of brokers that must acknowledge a
	// write before the relay considers the message successfully delivered.
	//
	// Supported values:
	//   "all"  - (Highest Durability) Waits for all in-sync replicas to acknowledge.
	//   "one"  - (Balanced) Waits only for the leader to acknowledge.
	//   "none" - (Maximum Speed) Does not wait for any acknowledgment (risks data loss).
	//
	//	Default: "all"
	//
	//	Note: In an Outbox Relay, "all" is the recommended setting to maintain
	//	strict "At-Least-Once" delivery guarantees.
	KafkaRequiredAcks string `mapstructure:"KAFKA_REQUIRED_ACKS"`
}

// Load initializes the Config struct by merging defaults, environment variables,
// and an optional .env file. It prioritizes system environment variables
// to ensure compatibility with Docker and Kubernetes secrets.
func Load() (*Config, error) {
	v := viper.New()

	// Initializing Safe Application Defaults
	// We default to "postgres" and "stdout" to ensure a functional starting point.
	v.SetDefault("STORAGE_TYPE", "postgres")
	v.SetDefault("STORAGE_TABLE_NAME", DefaultTableName)
	v.SetDefault("PUBLISHER_TYPE", "stdout")
	v.SetDefault("POLL_INTERVAL", "500ms")
	v.SetDefault("BATCH_SIZE", 100)
	v.SetDefault("LEASE_TIMEOUT", "3m")
	v.SetDefault("REAP_BATCH_SIZE", 100)
	v.SetDefault("SERVER_PORT", ":8080")
	v.SetDefault("ENVIRONMENT", Production)
	v.SetDefault("ENVIRONMENT", Production)

	// Default Retry Policies using the defined Jitter constant.
	v.SetDefault("RETRY_MAX_ATTEMPTS", 25)
	v.SetDefault("RETRY_BASE_DELAY", "1s")
	v.SetDefault("RETRY_MAX_DELAY", "24h")
	v.SetDefault("RETRY_JITTER", DefaultRetryJitter)

	//Nats Relay-Optimized Defaults
	v.SetDefault("NATS_PUBLISH_TIMEOUT", "5s")

	// Kafka Relay-Optimized Defaults
	// We set KAFKA_BATCH_SIZE to 1. Since our Relay Engine already batches
	// messages from the DB, setting this to 1 avoids the Kafka client adding
	// an extra artificial delay waiting for its internal buffer to fill.
	v.SetDefault("KAFKA_BATCH_SIZE", 1)
	v.SetDefault("KAFKA_BATCH_BYTES", 10485760) // 10MB default
	v.SetDefault("KAFKA_BATCH_TIMEOUT", "10ms")
	v.SetDefault("KAFKA_MAX_ATTEMPTS", 5)
	v.SetDefault("KAFKA_WRITE_TIMEOUT", "10s")
	v.SetDefault("KAFKA_READ_TIMEOUT", "10s")
	v.SetDefault("KAFKA_COMPRESSION", "snappy")
	v.SetDefault("KAFKA_REQUIRED_ACKS", "all")
	v.SetDefault("KAFKA_ASYNC", false)

	// Bind all struct tags to Viper's internal registry
	if err := bindEnvs(v, Config{}); err != nil {
		return nil, fmt.Errorf("failed to bind env vars: %w", err)
	}

	// Loading Optional Local Configuration from .env
	v.SetConfigFile(".env")
	v.SetConfigType("env")

	if err := v.MergeInConfig(); err != nil {
		// If the file is missing, we proceed silently as this is standard in PROD.
		// os.IsNotExist ensures we don't spam logs in containerized environments.
		if !os.IsNotExist(err) {
			println("WRN: Failed to read existing .env file:", err.Error())
		}
	} else {
		// Visual confirmation for developers that local overrides are active.
		if v.GetString("ENVIRONMENT") == string(Development) {
			println("INF: Configuration loaded from .env")
		}
	}

	// AutomaticEnv allows OS environment variables to override any file settings.
	// This is the core of the Twelve-Factor App methodology.
	v.AutomaticEnv()

	// Unmarshal processes the raw Viper data into the typed Config struct,
	// automatically parsing durations and integers.
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// bindEnvs tells Viper to look for environment variables for every
// field defined in the Config struct.
func bindEnvs(v *viper.Viper, iface interface{}) error {
	ifaceVal := reflect.ValueOf(iface)
	ifaceType := ifaceVal.Type()

	for i := 0; i < ifaceType.NumField(); i++ {
		field := ifaceType.Field(i)
		tag := field.Tag.Get("mapstructure")
		if tag != "" {
			if err := v.BindEnv(tag); err != nil {
				return err
			}
		}
	}
	return nil
}
