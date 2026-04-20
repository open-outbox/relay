# Open Outbox Relay

**Open Outbox Relay** is the official reference implementation of the [Open Outbox Specification](https://github.com/open-outbox/openoutbox-spec).

It is a high-performance daemon designed for the **Transactional Outbox Pattern**. By bridging your database and message brokers like **Kafka** or **NATS**, it ensures `at-least-once` delivery, solving distributed consistency challenges without the complexity of 2PC or distributed transactions.

---

## Features

* **Guaranteed Delivery:** `at-least-once` semantics ensure events are never lost, even during broker outages or relay crashes.
* **Database-Native Scaling:** Leverages database-native locking (like Postgres `SKIP LOCKED`) where available to support horizontal scaling without double-processing.
* **Self-Healing:** Built-in **Lease Reaper** automatically identifies and recovers "stuck" events from crashed worker instances.
* **Smart Retries:** Exponential backoff with jitter to prevent "thundering herd" issues during downstream outages.
* **Observability:** Native **OpenTelemetry** integration for distributed tracing and Prometheus metrics.
* **Provider Agnostic:** Pluggable storage (Postgres) and publishers (Kafka, NATS JetStream, Redis).

## Performance at Scale

* **Non-Blocking I/O:** Written in Go with a focus on zero-allocation paths in the event loop.
* **Drain Mode:** Automatically shifts from interval-based polling to high-speed "drain mode" during traffic bursts.
* **Resource Efficient:** Designed to run as a sidecar or a standalone microservice with a minimal memory footprint.

---

## Quick Start

Get the Relay up and running locally in under a minute using our pre-configured demo environment.

### 1. Launch the Environment

This starts a local stack including Postgres, NATS JetStream, and the Open Outbox Relay.

```bash
git clone https://github.com/open-outbox/relay.git
cd relay/examples/demo
docker compose up -d
```

### 2. Simulate an Event

Insert a record directly into the Postgres outbox table. The Relay will detect the new row and deliver it to the message broker instantly.

> -- Note: Requires Postgres 13+ or pgcrypto extension for gen_random_uuid()

```bash
docker compose exec postgres psql -U postgres -d postgres -c \
"INSERT INTO openoutbox_events (event_id, event_type, payload, partition_key)
VALUES (gen_random_uuid(), 'openoutbox.events.demo', '{\"id\": 123, \"name\": \"Alice\"}', '123');"
```

### 3. Verification

You can check the Relay logs to see the delivery confirmation:

```bash
docker compose logs relay
```

> 💡 **Ready for Production?**
> For database schema requirements, production hardening checklists, and deployment strategies,
> visit our [**Deployment Guide**](https://open-outbox.dev/guides/deployment).

---

## Reliability Guarantees

The Relay is designed for **At-Least-Once Delivery**.

* **No Data Loss:** Events are only marked as `DELIVERED` after the broker acknowledges receipt.
* **Idempotency:** In rare edge cases (e.g., a crash immediately after publishing but before the
DB update), the same event may be published twice. **Consumers must be idempotent.**

## Configuration

The Open Outbox Relay is designed to be cloud-native and is configured entirely via
environment variables. These are divided into core infrastructure, engine tuning
(polling logic), reliability, and publisher-specific settings (Kafka/NATS).

For a complete list of variables, default values, and tuning guides, see our documentation:

**[Read the Full Configuration Guide](https://open-outbox.dev/reference/configuration)**

## Documentation & Community

Stay connected and help us improve the Open Outbox ecosystem:

* **[Contribution Guide](./CONTRIBUTING.md)**: Ready to help? Check out our guide on setting up your local environment, running tests with the `Makefile`, and our pull request process.
* **[Changelog](./CHANGELOG.md)**: View a detailed list of changes, improvements, and bug fixes for every release.
* **[Open Outbox Specification](https://github.com/open-outbox/spec)**: Learn more about the standard this relay implements.

---

## Project Status & Roadmap

Open Outbox is currently in **Public Beta** (`v0.1.0-beta`). We are stable enough for pilot production use and are actively seeking feedback as we head toward a v1.0 release.

Check out our [**Project Roadmap**](./ROADMAP.md) to see what's coming next, including:

* Support for MySQL database
* Adding new broker support
* Dashboard for event monitoring
* Ordering support

---

## License

[MIT License](./LICENSE) — Created by the Open Outbox Team.
