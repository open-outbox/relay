# Changelog

All notable changes to the **OpenOutbox Relay** will be documented in this file. 
This project adheres to [Semantic Versioning](https://semver.org/).

## [1.0.0-beta.1] - 2026-04-10

### ✨ Added
- **Core Engine:** High-performance event loop with "Drain Mode" for high-throughput scenarios.
- **Postgres Storage:** Full implementation using `FOR UPDATE SKIP LOCKED` for horizontal scalability.
- **Kafka Publisher:** Support for synchronous and asynchronous event publishing.
- **NATS Publisher:** JetStream integration for reliable message delivery.
- **CLI Tooling:** Initial `prune` command for database maintenance.
- **Observability:** Native OpenTelemetry support for traces and Prometheus metrics.
- **Self-Healing:** Lease Reaper mechanism to recover events from crashed relay instances.

### ⚙️ Changed
- Refactored internal storage interfaces to support future MySQL and MongoDB providers.
- Optimized database indexing strategy for the standard `outbox_events` schema.

### 🛡️ Security
- Implemented context-aware timeouts for all database and network operations.

---

> **Note:** As this is a Beta release, breaking changes may occur in the configuration schema before the stable 1.0.0 release.
