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
git clone https://github.com/open-outbox/relay.git
cd relay/benchmark
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

### 4. Engine Tuning

Beyond the broker connection strings, you can
fine-tune the Relay's engine behavior. Adjust these
in your `.env` file to match your performance
requirements:

```bash
# Duration that the relay pause in case of empty batches (When the relay is faster than the producer)
POLL_INTERVAL=500ms

# Max events to process in a single iteration (Higher = better throughput)
BATCH_SIZE=1000

# Duration before a "DELIVERING" event is considered stuck/crashed
LEASE_TIMEOUT=3m

# Number of expired leases to reset per cleanup cycle
REAP_BATCH_SIZE=100

# Wait time between connection attempts to the broker (NATS/Kafka) during startup
PUBLISHER_CONNECT_RETRY_INTERVAL=5s

# Frequency of background health probes (determines outage reaction speed)
HEALTH_CHECK_INTERVAL=5s
```

### 5. Run the Stack

> Make sure your system has enough memory according
> to the [./postgres.conf](postgres) config.
> Otherwise the postgres volume will be created
> without the required schemas. In this case drop the postgres volume and recreate it after you
> adjusted your memory setting.

Before running, ensure you build the latest versions of the Relay and Producer images:

```bash
make bench-build
```

Then, start the core infrastructure along with the profile for your selected broker:

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

> [!IMPORTANT]
> **Broker Persistence**: These commands use the `-setup` containers
> to provision the necessary topics/streams. In case you fully deleted the
> broker containers (using `make bench-clean` or `docker compose down -v`)
> use `make bench-XXX` to restart the brokers properly.

### 6. Start Seeding

Use the producer tool to flood the database. There are two primary modes for benchmarking:

#### **A. Bulk Seeding (`produce-seed`)**

Injects a fixed number of events and exits. Use this to test how the Relay drains a
massive, pre-existing backlog.

```bash
# Example: Inject 200,000 events
make produce-seed -- --count 200000 --payload-size 200
```

**Note**: To see the options for producer commands Use:

```bash
make produce-help
```

#### B. Continuous Benchmarking (produce-bench)

Produces events continuously at a set interval. Use this to measure "Sustained Throughput" and identify the Relay's breaking point.

```bash
# Example: Produce 100 events every 10ms (approx. 10,000 EPS)
make produce-bench -- --batch-size 100 --interval 10ms
```

* `--interval`: The pause between production batches (default: 1s).

**Pro-Tip**: You can see all available flags (including --storage-url and --topic) by running make produce-help.

## 7. Concurrent Testing Scripts

To simulate production-grade workloads and identify the "breaking point" of your database and broker
clusters, use the concurrent orchestration scripts. These scripts bypass single-process bottlenecks
by spawning multiple isolated workers.

### A. Parallel Bulk Seeding (`seed.sh`)

While `make produce-seed` is suitable for baseline testing,
`seed.sh` does a distributed seeding. Use this if you want to
quickly fill your database before testing.

```bash
# Example: Launch 10 parallel producers to seed 50M events total
./scripts/seed.sh --iterations 10 --count 5000000 --payload 200 --batch 10000
```

| Parameter | Short Description | Default |
| :--- | :--- | :--- |
| --iterations -i | Number of concurrent shell-level processes | 10 |
| --count -c | Events generated per worker | 5,000,000 |
| --batch -b | Events per DB transaction | 10,000 |
| --payload -p | Size of each payload in bytes | 200 |
| --interval -t | Sleep between each batch in duration format: "1s, 100ms, etc." | 1s |

### B. Parallel Pressure Benchmarking (bench.sh)

While `make produce-bench` is suitable for baseline testing,
`bench.sh` simulates a distributed producer environment.
This is critical for testing Lock Contention in the Outbox table.

```bash
# Run 5 parallel benchmarkers with customized batching
./scripts/bench.sh -i 5 -p 256 -b 5000
```

| Parameter | Short Description | Default |
| :--- | :--- | :--- |
| --iterations -i | Number of concurrent shell-level processes | 10 |
| --batch -b | Events per DB transaction | 10,000 |
| --payload -p | Size of each payload in bytes | 200 |
| --interval -t | Sleep between each batch in duration format: "1s, 100ms, etc." | 1s |

> **!CAUTION**
> Resource Exhaustion: Running high iterations (e.g., -i 20) may exhaust
> the Docker Bridge network's available ports or trigger Database connection
> limits (max_connections). Monitor your docker stats during execution.
>
> ```bash
> docker stats $(docker compose ps -q)
> ```


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

The benchmark suite includes a pre-provisioned Grafana instance.

1. Open [http://localhost:3000](http://localhost:3000) in your browser.
2. Navigate to the **Dashboards** section.
3. Open the **"Open Outbox"** folder.
4. Select the **"Open Outbox Dashboard"** dashboard.

This dashboard provides real-time visualization of your EPS, Batch Efficiency, and SLO compliance. It is the same dashboard available on [Grafana.com](https://grafana.com/grafana/dashboards/25221).

## Cleanup

To reset the environment and wipe all data/topics:

```bash
make bench-clean
```

## Performance Tuning

To push for maximum EPS (Events Per Second), try these `.env` adjustments:

* `BATCH_SIZE=1000`: Increases throughput by reducing DB roundtrips.
* `REAP_BATCH_SIZE=500`: Speeds up recovery of expired leases.

## Scaling: Running Multiple Relays

The OpenOutbox Relay is designed to be horizontally scalable. You can run multiple instances to increase throughput or test high-availability scenarios (e.g., one relay crashing while others continue).

To scale the relay, use the `--scale` flag with Docker Compose:

```bash
# Example: Run 3 parallel relay instances
docker compose up -d --scale relay=3
```

**Important Scaling Notes:**

* **Relay IDs**: Each instance must have a unique identity for the "Claiming" logic to work correctly. In the relay, this is handled automatically via the container's hostname. Make sure not the set the `RELAY_ID` environment variable manually when running multiple instances.

* **Database Load**: While scaling relays increases processing power, it also increases the number of concurrent `SELECT FOR UPDATE` queries on your database. Monitor your DB CPU and Lock contention when scaling beyond 5+ instances.

* **Visualization**: The Grafana dashboard will automatically detect new instances and show them as separate rows in the Relay Operational State (State Timeline) panel.

## Kafka metrics

The `./scripts/kafka_metrics.sh` can be used to monitor the **EPS** and **Throughput**
of kafka in realtime.
