# ROADMAP

🟢 Phase 1: Core Infrastructure (The "Skeleton")

[X] Dependency Injection (uber-go/dig): Refactor main.go to use a container. This allows us to "swap" a NATS publisher for a Kafka one just by changing the provider function.

[X] Structured Logging (uber-go/zap): Move away from log.Printf. We need JSON logging with fields (e.g., {"level":"info", "event_id":"...", "module":"engine"}).

[X] Dockerization: Create a multi-stage Dockerfile and a production-ready docker-compose.yml.

[X] Kafka: The industry heavy-hitter.

[X] Redis: Using Redis Streams or Pub/Sub.

[X] Prometheus Metrics: Add a /metrics endpoint to track:

[X] OpenTelemetry (OTEL): Add tracing support so we can see an event move from the DB through the Relay into the Broker in a single trace.

[ ] Viper Evolution: fully enable the remote providers for Consul and Etcd.

🟡 Phase 2: The Multi-Store Ecosystem (Pluggability)
[ ] Additional Databases: * [ ] MySQL: Implement the relay.Storage interface for MySQL/MariaDB.

[ ] MongoDB: (Optional but popular) For document-based outboxes.

[ ] Additional Publishers:

[ ] RabbitMQ: For traditional enterprise messaging.

[ ] HTTP/Webhooks: To call external APIs directly from the outbox.

🔴 Phase 3: Observability & Reliability (The "Production" Polish)

[ ] Dead Letter Logic Finalization: Add a "Max Retries" policy and a way to move "Permanently Failed" messages to a separate table for manual review.

🔵 Phase 4: Project Spec & Documentation
[ ] The "Outbox Schema" Spec: Define a standard SQL schema that users must follow to use the relay.

[ ] CLI Tooling: A small CLI to "retry" failed messages or "purge" old completed ones.

[ ] GitHub Actions: CI/CD for automated testing and linting
