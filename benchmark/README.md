# Open Outbox Relay Benchmark Suite

This directory contains the official benchmarking rig for the OpenOutbox Relay. Use this suite to measure throughput (Events Per Second), test broker-specific latency, and visualize performance via the OpenTelemetry stack.

## Prerequisites

* **Docker & Docker Compose**: Essential for running integration tests against real instances of Postgres, Kafka, and NATS.
* **GNU Make** : Used to orchestrate builds, environment setup, and lifecycle management.

---

## Execution Steps

### 1. Initialize Environment

Clone the repository and move into the benchmark directory:

```bash
git clone [https://github.com/openoutbox/openoutbox.git](https://github.com/openoutbox/openoutbox.git)
cd openoutbox/relay/benchmark
cp ../.env.example .env
```

### 2. Configure Infrastructure

Open the .env file and update the following settings to allow the Relay to communicate with the Docker-managed services:

**Storage**:

```bash
# Point to the internal Docker Postgres service
STORAGE_URL=postgres://postgres:postgres@postgres:5432/postgres
```

**Observability (OpenTelemetry)**:

To visualize performance in Jaeger, and Grafana, set OTEL configurations accordingly:

```bash
OTEL_TRACES_EXPORTER=otlp
OTEL_METRICS_EXPORTER=otlp
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317
OTEL_EXPORTER_OTLP_PROTOCOL=grpc
OTEL_METRIC_EXPORT_INTERVAL=1000
```

### 3. Choose Your Publisher

Modify the `PUBLISHER_TYPE` and `PUBLISHER_URL` based on the broker you want to benchmark:

| Broker | PUBLISHER_TYPE | PUBLISHER_URL |
| :--- | :--- | :--- |
| NATS JetStream | nats | nats://nats:4222 |
| Kafka | kafka | kafka:9092 |
| Redis | redis | redis://redis:6379 |
| Stdout | stdout | (Leave empty) |
| Null * | null | (Leave empty) |

> \* **Why use the null publisher?**
>
> The null publisher is a "no-op" driver. It
> discards all events immediately after they are
> claimed from the database.
>
> Use this to establish a performance baseline.
> It helps you determine the maximum theoretical
> throughput of the Relay's polling and claiming
> logic by completely removing the latency of
> external brokers (Kafka, NATS, etc.) from the
> equation. If your null benchmark is slow, the
> bottleneck is likely your database indices or
> theyour POLL_INTERVAL settings.

### 4. Run the Stack

Start the core infrastructure along with the profile for your selected broker:

```bash
# Example for Kafka
make bench-kafka
```

```bash
# Example for NATS
make bench-nats
```

```bash
# Example for Redis
make bench-redis
```

> **Note**: The stack automatically handles topic/stream
> creation via the -setup containers.

### 5. Start Seeding

Flood the database with events to begin the
benchmark. You can pass arguments to the producer
using the -- separator:

```bash
# produces 100 events every 10ms
make produce-bench -- --batch-size 100 --interval 10ms
```

or

```bash
# produces 100000 events and stop
make produce-seed -- --count 100000
```

**Note**: To see the options for producer commands Use:

```bash
# See the produce help
make produce-help
```

```bash
# See the produce-bench help
make produce-bench -- --help
```

```bash
# See the produce-seed help
make produce-seed -- --help
```

## Monitoring Results

### Check Relay Throughput

Monitor the logs to verify delivery success and batch counts:

```bash
docker compose logs -f relay
```

### Visualize Traces (Jaeger)

1. Open [http://localhost:16686](http://localhost:16686) in your browser.

2. Select Service: **openoutbox-relay**

3. Select an operation e.g. _Engine.Process_

This will show you exactly how long each phase
(Database Claiming vs. Broker Publishing) is
taking for every batch.

### Metrics (Grafana)

ToDo

## Cleanup

To reset the environment and wipe all data/topics:

```bash
make bench-clean
```

## Performance Tuning

To push for maximum EPS (Events Per Second), try these `.env` adjustments:

* `BATCH_SIZE=1000`: Increases throughput by reducing DB roundtrips.
* `REAP_BATCH_SIZE=500`: Speeds up recovery of expired leases.
