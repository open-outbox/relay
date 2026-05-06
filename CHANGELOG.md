# Changelog

All notable changes to the **Open Outbox Relay** will be documented in this file.
This project adheres to [Semantic Versioning](https://semver.org/).

## [1.0.0] - 2026-05-04

### ✨ Added

- **Stable Release:** Finalized core API and environment variable schema.
- **Core Engine:** High-performance event loop with "Drain Mode" for high-throughput scenarios.
- **Postgres Storage:** Full implementation using `FOR UPDATE SKIP LOCKED` for horizontal scalability.
- **Kafka Publisher:** Support for synchronous and asynchronous event publishing.
- **NATS Publisher:** JetStream integration for reliable message delivery.
- **Redis Publisher:** Redis streams integration for reliable message delivery.
- **Observability:** Native OpenTelemetry support for traces and Prometheus metrics.
- **Resilience:** Automatic failover and retry logic for remote config providers.
- **Integration Testing:** Added Testcontainers-based suite for verifying multi-infrastructure setups.
- **Self-Healing:** Lease Reaper mechanism to recover events from crashed relay instances.
- **Benchmark-suite:** Added `benchmark` directory for easier performance testing.
- **Documentation:** Added full documentation to the project.

### ⚙️ Changed
- **Optimization:** Improved batch claiming query performance for large PENDING backlogs.

## [1.0.0-beta.1] - 2026-04-10

### ✨ Added

- Initial beta release with core Postgres and Kafka/NATS support.
