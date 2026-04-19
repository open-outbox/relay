# Roadmap

The Open Outbox Relay is evolving from a robust core into a high-performance, universal distribution engine for the Transactional Outbox Pattern.

## Phase 1: Foundation & Observability (Completed)

* **Dependency Injection:** Modular architecture using `uber-go/dig` for swappable publishers and storage engines.
* **Structured Logging:** High-performance, production-ready JSON logging with `uber-go/zap`.
* **Containerization:** Multi-stage Docker builds and `docker-compose` orchestration for rapid local development.
* **Core Publishers:** Native support for high-throughput **Kafka** and cloud-native **NATS**.
* **Distributed Tracing:** Full **OpenTelemetry (OTel)** integration for end-to-end visibility of event lifecycles.

## Phase 2: The Multi-Engine Ecosystem (In Progress)

* **MySQL Support:** Implement the `relay.Storage` interface for MySQL/MariaDB.
* **RabbitMQ Publisher:** Support for traditional enterprise messaging via AMQP.
* **Redis Integration:** Adding Redis (Streams/Pub-Sub) as a high-speed storage and distribution option.
* **Webhook Relay:** Direct **HTTP/Webhook** support to trigger external APIs directly from the outbox.
* **Enterprise Config:** Enable remote configuration providers for **Consul** and **Etcd**.

## Phase 3: Enterprise Hardening & Scale

* **Distribution:** Automated binary releases for Linux, macOS, and Windows via GitHub Actions.
* **Orchestration:** Official **Helm Charts** and K8s manifests for production-grade deployments.
* **Strict Ordering:** Enhanced support for strictly ordered message delivery and partitioned relaying.
* **Security & Encryption:** Native TLS/mTLS support across all storage and publisher drivers.
* **Throughput Optimization:** Advanced tuning for high-frequency polling and publisher batching.
* **Advanced Observability:** Pre-built Grafana dashboards for standard OTel metrics.

---

> **Note:** We are actively looking for contributors to help with Phase 2 integrations! See [CONTRIBUTING.md](./CONTRIBUTING.md) for details.
