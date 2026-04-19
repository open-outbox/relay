# Open Outbox Relay

**OpenOutbox Relay** is the official reference implementation of the [Open Outbox Specification](https://github.com/open-outbox/openoutbox-spec).

It is a high-performance daemon designed for the **Transactional Outbox Pattern**. By bridging your database and message brokers like **Kafka** or **NATS**, it ensures `at-least-once` delivery, solving distributed consistency challenges without the complexity of 2PC or distributed transactions.

---

## ✨ Features

* **Guaranteed Delivery:** `at-least-once` semantics ensure events are never lost, even during broker outages or relay crashes.
* **Database-Native Scaling:** Leverages database-native locking (like Postgres `SKIP LOCKED`) where available to support horizontal scaling without double-processing.
* **Self-Healing:** Built-in **Lease Reaper** automatically identifies and recovers "stuck" events from crashed worker instances.
* **Smart Retries:** Exponential backoff with jitter to prevent "thundering herd" issues during downstream outages.
* **Observability:** Native **OpenTelemetry** integration for distributed tracing and Prometheus metrics.
* **Provider Agnostic:** Pluggable storage (Postgres) and publishers (Kafka, NATS JetStream, Redis).

## 📊 Performance at Scale

* **Non-Blocking I/O:** Written in Go with a focus on zero-allocation paths in the event loop.
* **Drain Mode:** Automatically shifts from interval-based polling to high-speed "drain mode" during traffic bursts.
* **Resource Efficient:** Designed to run as a sidecar or a standalone microservice with a minimal memory footprint.

---

## 🚀 Quick Start

### 1. Database Schema

The Relay requires an `openoutbox_events` table. Run the standard DDL found in:
[`schema/postgres/open-outbox.sql`](./schema/postgres/open-outbox.sql)

### 2. Run with Docker

```bash
docker run -d \
  --name openoutbox-relay \
  -e STORAGE_URL="postgres://user:pass@localhost:5432/db" \
  -e PUBLISHER_TYPE="kafka" \
  -e PUBLISHER_URL="localhost:9092" \
  openoutbox/relay:latest
```

Or with `docker-compose`:

```yaml
---

services:
  relay:
    image: openoutbox/relay:latest
    container_name: openoutbox-relay
    environment:
      - STORAGE_TYPE=postgres
      - STORAGE_URL=postgres://postgres:postgres@db:5432/postgres
      - PUBLISHER_TYPE=kafka
      - PUBLISHER_URL=kafka:9092
    restart: always
```

### Try it Out (Live Environment)

A pre-configured environment is provided in the [`/examples/demo`](./examples/demo)
directory. This allows you to see the Relay in action without any manual setup.

To start the infrastructure (Postgres, NATS, and the Relay):

```bash
cd examples/demo
docker compose up -d
```

**Simulate an Event**
Once the services are up, you can simulate a business event by inserting a
record directly into the Postgres "outbox" table. The Relay will detect
it and deliver it to NATS instantly.

```bash
docker compose exec postgres psql -U postgres -d postgres -c \
"INSERT INTO openoutbox_events (event_id, event_type, payload, partition_key)
VALUES (gen_random_uuid(), 'outbox.events.demo', '{\"id\": 123, \"name\": \"Alice\"}', '123');"
```

## Operational Commands

The Relay includes a built-in CLI for maintenance, cleanup, and manual intervention.

### Pruning Historical Data

To keep the outbox table performant, you should periodically prune successfully delivered
or exhausted (dead) events.

> **Performance Note:** Pruning requires specific indices to handle high-volume tables.
Before running your first prune, ensure you have executed the standard DDL
located in: [`schema/postgres/maintenance.sql`](./schema/postgres/maintenance.sql).

---

#### Using Docker Compose (Recommended)

If you are using the example [`docker-compose.yml`](./examples/demo/docker-compose.yml),
run the maintenance command via the `cli` service. This ensures the CLI uses the same
network and credentials as the Relay.

**Dry Run (Simulation)**:

```bash
docker compose run --rm cli prune --delivered-age 7d --dead-age 30d --dry-run
```

**Execute Pruning**:

```Bash
docker compose run --rm cli prune --delivered-age 7d --dead-age 30d
```

#### Using Pure Docker

If you prefer to run the container without Compose, you must specify the
CLI binary path and your database connection string as an environment variable.

```bash
docker run --rm \
  --network open-outbox-network \
  -e STORAGE_URL="postgres://postgres:postgres@postgres:5432/postgres" \
  --entrypoint "/app/cli"
  openoutbox/relay:latest \
  prune --delivered-age 7d --dead-age 30d
```

#### Automation via Cron

To automate maintenance, add an entry to your crontab. Using the -T flag is essential
for non-interactive environments.

```bash
# Run daily at 2:00 AM
0 2 * * * cd /path/to/project && docker compose run --rm -T cli prune --delivered-age 7d --dead-age 30d
```

---

## 🛡️ Reliability Guarantees

The Relay is designed for **At-Least-Once Delivery**.

* **No Data Loss:** Events are only marked as `DELIVERED` after the broker acknowledges receipt.
* **Idempotency:** In rare edge cases (e.g., a crash immediately after publishing but before the
DB update), the same event may be published twice. **Consumers must be idempotent.**

## ⚙️ Configuration

The OpenOutbox Relay is designed to be cloud-native and is configured entirely via
environment variables. These are divided into core infrastructure, engine tuning
(polling logic), reliability, and publisher-specific settings (Kafka/NATS).

For a complete list of variables, default values, and tuning guides, see our documentation:

**[Read the Full Configuration Guide](https://open-outbox.dev/reference/configuration)**

## 📚 Documentation & Community

Stay connected and help us improve the OpenOutbox ecosystem:

* **[Contribution Guide](./CONTRIBUTING.md)**: Ready to help? Check out our guide on setting up your local environment, running tests with the `Makefile`, and our pull request process.
* **[Changelog](./CHANGELOG.md)**: View a detailed list of changes, improvements, and bug fixes for every release.
* **[OpenOutbox Specification](https://github.com/open-outbox/openoutbox-spec)**: Learn more about the standard this relay implements.

---

## Project Status & Roadmap

OpenOutbox is currently in **Alpha** (`v0.1.x`). We are actively working on scaling the Relay and adding more producer drivers.

Check out our [**Project Roadmap**](./ROADMAP.md) to see what's coming next, including:

* Support for MySQL datavase
* Adding new broker support
* Dashboard for event monitoring
* Ordering support

---

## License

[MIT License](./LICENSE) — Created by the OpenOutbox Team.
