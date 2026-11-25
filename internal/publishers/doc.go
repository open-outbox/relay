// Package publishers provides concrete implementations for various message
// delivery backends.
//
// The broker publishers live in sub-packages:
//   - kafka: For publishing messages to Apache Kafka topics.
//   - nats: For publishing messages to NATS JetStream streams.
//   - redis: For publishing messages to Redis Streams.
//
// This package holds the ones with no external dependencies:
//   - Stdout: A basic publisher for logging events to standard output, useful for debugging.
//   - Null: A no-op publisher, primarily used for performance benchmarking the relay engine
//     without external system latency.
//
// Each publisher implementation is designed to handle message serialization,
// transport-specific configurations, and robust error handling. A key aspect
// is determining if a publishing error is transient (retryable) or permanent,
// which informs the relay engine's retry policies.
//
// This package also includes an `Instrumented` publisher which wraps any
// `relay.Publisher` to add OpenTelemetry metrics and tracing automatically.
package publishers
