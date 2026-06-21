# Changelog

All notable changes to the **Open Outbox Relay** will be documented in this file.
This project adheres to [Semantic Versioning](https://semver.org/).

## [1.1.0] - 2026-06-22

### ✨ Added

- **Kafka Enterprise Security:** Full support for TLS, mutual TLS (mTLS), and SASL authentication mechanisms (`PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512`).
- **Flexible Security Ingestion:** Added capability to parse certificates and keys from files (`file://`), Base64 strings (`base64://`), or raw inline PEM blocks.
- **Configurable TLS Enforcements:** Exposed configuration options for minimum TLS versions (1.0 to 1.3) and Server Name Indication (SNI).
- **Remote Configuration:** Support for fetching configuration from `Consul` and `etcd3`.
- **Observability Middleware:** Introduced `Instrumented` storage to decouple telemetry from database logic and engine.
- **New Metrics:** Added `openoutbox_events_reaped` counter to track event recovery activity.
- **Enhanced Dashboards:** Added Grafana panels for Reaping Rates and specific storage operations (latency/throughput).
- **OTel Infrastructure:** Upgraded OpenTelemetry Collector configuration to use current (non-deprecated) schema.

### ⚙️ Changed

- **Storage Interface**: `MarkDeliveredBatch` and `MarkFailedBatch` now return the number of affected rows (`int64`).
- **Observability**: Moved lease expiration warnings from the database drivers to the `InstrumentedStorage` layer for better separation of concerns.
- **Clean Architecture:** Removed `relay_id` from the global Storage interface, delegating identity handling to specific implementations.
- **Documentation Update:** Rewrote the project `Quick Start` guide, adding support for telemetry, observability and
continuous load generation.

### 🛡️ Fixed

- **Telemetry Accuracy:** Ensured lease expiration is tracked within the instrumentation decorator rather than the core storage implementation.
- **Integration Test Conflicts:** Resolved an issue where local environment variables were unintentionally overriding remote configuration providers during testing.
- **Engine Initialization:** Resolved a bug where the `EnableStats` flag was uninitialized in the engine container.

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
