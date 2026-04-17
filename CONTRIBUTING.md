# Contributing to OpenOutbox Relay

First off, thank you for considering contributing! It’s people like you who make OpenOutbox a reliable standard for everyone.

We love contributions! If you want to help, please check our [**Roadmap**](./ROADMAP.md) to see our prioritized features and "help wanted" areas.

## Local Development Setup

Before starting development make sure you have the following setup.

### Prerequisites

* **Go 1.22+**: The core runtime for the relay.
* **Docker & Docker Compose**: Essential for running integration tests against real instances of Postgres, Kafka, and NATS.
* **GNU Make** : Used to orchestrate builds, environment setup, and lifecycle management.

### How to Run the Service Locally

The easiest way to see the Relay in action is to use the provided local stack.

1. Clone the repository

    ```bash
    git clone https://github.com/open-outbox/relay.git
    cd relay
    ```

2. Create the required .env file

    ```bash
    cp .env.example .env
    ```

3. Spin up the storage using docker compose and create `outbox_events` table:

    ```bash
    make up-postgres
    make db-init
    ```

4. Run the Relay in development mode:

    ```bash
    make run
    ```

5. Produce test events (use another terminal window):

    ```bash
    make produce
    ```

In the first terminal, you should now see that events are being published to `stdout` (the test publisher).

### Using NATS publisher

In order to use NATS as the publisher:

1. **Update the `.env` file**:

    ```bash
    PUBLISHER_TYPE=nats
    PUBLISHER_URL=nats://localhost:4222
    ```

2. **Ensure NATS services are running** in docker-compose:

    ```bash
    make up-nats
    make up-nats-box
    ```

3. **Configure JetStream**:

    ```bash
    make nats-setup
    make nats-info
    ```

4. **Run the producer** to generate test events:

    ```bash
    make produce
    ```

5. **Run the relay** in another terminal:

    ```bash
    make run
    ```

6. **Monitor the stream** to view published messages:

    ```bash
    make nats-view
    ```

### Using Kafka publisher

In order to use Kafka as the publisher:

1. **Update the `.env` file**:

    ```bash
    PUBLISHER_TYPE=kafka
    PUBLISHER_URL=localhost:9092
    ```

2. **Ensure the Kafka service is running**:

    ```bash
    make up-kafka
    ```

3. **Initialize the Kafka topic**:

    ```bash
    make kafka-setup
    make kafka-info
    ```

4. **Run the producer**:

    ```bash
    make produce
    ```

5. **Run the relay**:

    ```bash
    make run
    ```

6. **Tail the Kafka topic** to view published messages:

    ```bash
    make kafka-tail
    ```

> **Note**: The infrastructure stack includes a Kafka **UI service**. service.
> If you prefer a graphical interface for inspecting messages, run
> `make up-kafka-ui` and visit [http://localhost:8081](http://localhost:8081)

## Local development environment variables

To control the producer, or the publishers you can use these
environment variables. For the full list of environment variables and their usage refer
to the Configuration section of the [README.md](./README.md)

| Variable | Description | Default |
| :--- | :--- | :--- |
| `LOCAL_TEST_TOPIC` | Base name for publisher subjects/topics | `outbox.events.v1` |
| `LOCAL_NATS_STREAM` | JetStream stream name for NATS mode | `OUTBOX_EVENTS` |
| `LOCAL_OTEL_TEST_TRACE_COUNT` | Traces to simulate during `make test-otel` | `100` |
| `LOCAL_PRODUCER_BATCH_SIZE` | Records per batch inserted by test producer | `10000` |
| `LOCAL_PRODUCER_INTERVAL` | Interval between test batch insertions | `1s` |

> **Note**: if you change the `LOCAL_TEST_TOPIC`, and `LOCAL_NATS_STREAM` variables,
> you need to recreate the new topic or stream in NATS and Kafka,
> and also restart the `producer` to produce the correct events.

## Observability Infrastructure

The "deployments/infra-docker-compose.yml" provides a full observability suite.

| Service | Local URL | Purpose |
| :--- | :--- | :--- |
| **Kafka UI** | `http://localhost:8081` | Inspect topics and messages. |
| **Jaeger** | `http://localhost:16686` | Trace event lifecycle from DB to Broker. |
| **Grafana** | `http://localhost:3000` | Pre-configured dashboards for Relay performance. |
| **Prometheus** | `http://localhost:9090` | Query raw metrics and check OTel ingestion. |

**You can run the full stack with the following command:**

```bash
make up
```

**To run only the observability stack:**

```bash
make up-otel-collector up-jaeger up-grafana up-prometheus up-kafka-ui
```

## ⌨️ Makefile Commands

We use a `Makefile` to standardize common tasks. Please use these commands to ensure your environment matches our CI/CD pipeline.

```bash
make help
```

Prints the list of all available commands with the explanation of what they do.

### Development & Execution

These commands help with local development and testing.

| Command | Description |
| :--- | :--- |
| `make run` | Run the Relay service locally using Go. |
| `make produce` | Run the Producer to generate dummy events for testing. |
| `make build` | Compile the Relay and CLI into binaries in the `./bin` directory. |
| `make clean` | Remove build binaries and clear Go test cache. |
| `make docker-build` | Builds the production-ready OCI container image. |
| `make gen-api` | Generates the API documentation in the docs directory, to be tested by `make docs-dev`. |
| `make docs-dev` | Runs the documentation in develoopment mode. Should run after `make gen-api`. |

### Tooling Setup

This is required to make the [Quality and Linting](./#quality-and-linting) commands work.

| Command | Description |
| :--- | :--- |
| `make setup` | Install required local development tools and git hooks e.g. `pre-commit`, `goimports`, etc. |

### Quality & Linting

commands used to check for code quality, and testing, required for local development.

| Command | Description |
| :--- | :--- |
| `make fmt` | Format code, organize imports, and enforce 100-char line limits. |
| `make lint` | Runs `fmt` and then golangci-lint to catch code quality issues. |
| `make test` | Run all project tests with the race detector enabled. |

### Infrastructure

These commands help you to run infrastructure tools using docker-compose
for local development and testing.

| Command | Description |
| :--- | :--- |
| `make up` | Spin up all infrastructure (Postgres, Kafka, NATS, OTel). |
| `make up-%` | Spin up a specific service (e.g., make up-kafka). |
| `make down` | Shut down all infrastructure and remove networks. |
| `make down-%` | Stop a specific service (e.g., make down-postgres). |
| `make logs` | Follow logs for all running containers. |
| `make logs-%` | Follow logs for a specific service (e.g., make logs-nats). |
| `make ps` | Show status of all project containers. |

For the list of available infrastructure services refer to [Infrastructure Services](./#infrastructure-services).

### NATS Management

commands required for working with NATS in local development. These commands
use `LOCAL_TEST_TOPIC` and `LOCAL_NATS_STREAM` environment variables for the
NATS stream and topic names.

| Command | Description |
| :--- | :--- |
| `make nats-setup` | Create the JetStream stream and bind the subject pattern. |
| `make nats-view` | View messages currently in the JetStream. |
| `make nats-info` | Show detailed metadata and sequence numbers for the stream. |

### Kafka Management

commands required for working with Kafka in local development. These commands
use `LOCAL_TEST_TOPIC` environment variable for the
Kafka topic name.

| Command | Description |
| :--- | :--- |
| `make kafka-setup` | Ceate the required Kafka topic with 3 partitions. |
| `make kafka-list` | List all existing topics in the Kafka cluster. |
| `make kafka-info` | Deep dive into the configuration of the topic. |
| `make kafka-tail` | Tail messages from the beginning of the topic in real-time. |

### Observability & Database

commands used to create the `outbox_events` table in Postgres, and test the `otel` collector.

| Command | Description |
| :--- | :--- |
| `make db-init` | Detects STORAGE_TYPE and applies the correct SQL schema. |
| `make test-otel` | Send a batch of test traces to verify the OTel pipeline. |

## 🚦 Pull Request Standards

* **Conventional Commits**: We use [Conventional Commits](https://www.conventionalcommits.org/) (e.g., `feat:`, `fix:`, `docs:`). This allows us to automate our changelog.
* **Coverage**: New features must include unit or integration tests. We aim for high coverage on the "Drain" and "Lease" logic.
* **Single Responsibility**: Keep PRs focused. If you find a bug while adding a feature, please submit two separate PRs.

## 🏗 Architecture Principles

* **Statelessness**: The Relay must never store state in memory that isn't backed by the database.
* **Context Awareness**: All database and network calls must respect the `context.Context` for graceful shutdowns.
* **Provider Isolation**: Logic for specific databases or brokers must stay within their respective `/internal/storage` or `/internal/publisher` packages.

## Writing Integration Tests

Since the Relay interacts with external systems, unit tests aren't always enough. When adding a new storage or publisher provider:

1. Add a new test file in the `test/integration` directory.
2. Use the `testhelpers` package to spin up the required container.
3. Ensure the test cleans up after itself to keep the environment stable for the next run.
