# --- Load environment variables from .env if it exists ---
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

# --- Configuration & Defaults ---
BINARY_NAME         := openoutbox-relay
COMPOSE_FILE        := deployments/docker-compose.yaml
MAIN_PACKAGE        := ./cmd/relay/main.go
PRODUCER_PKG        := ./cmd/producer/main.go
KAFKA_URL           := kafka:9092
NATS_URL            := nats:4222

# Map LOCAL_ env vars to internal Make variables for cleaner targets
TOPIC_NAME          := $(LOCAL_TEST_TOPIC)
NATS_STREAM         := $(LOCAL_NATS_STREAM)
OTEL_TRACE_COUNT    := $(LOCAL_OTEL_TEST_TRACE_COUNT)

.PHONY: all build run producer test clean fmt lint up down setup docs help ps logs

# Default target: Run the full development pipeline
all: setup fmt lint build

# ==========================================
# Development & Execution
# ==========================================

# Run the Relay service locally using Go
run:
	go run $(MAIN_PACKAGE)

# Run the Producer to generate dummy events for testing
producer:
	go run $(PRODUCER_PKG)

# Compile the Relay into a binary in the bin/ directory
build:
	mkdir -p bin
	go build -o bin/$(BINARY_NAME) $(MAIN_PACKAGE)

# Remove build binaries and clear Go test cache
clean:
	rm -rf bin/
	go clean -testcache

# ==========================================
# Quality & Linting
# ==========================================

# Format code, organize imports, and enforce 100-char line limits
fmt:
	goimports -w .
	golines . -w --max-len=100
	go mod tidy

# Run golangci-lint to catch code quality issues
lint: fmt
	golangci-lint run ./...

# Run all project tests with the race detector enabled
test:
	go test -v -race ./...

# ==========================================
# Infrastructure (Docker)
# ==========================================

# Spin up all infrastructure (Postgres, Kafka, NATS, OTel)
up:
	docker-compose -f $(COMPOSE_FILE) up -d

# Spin up a specific service (e.g., make up-kafka)
up-%:
	docker-compose -f $(COMPOSE_FILE) up -d $*

# Shut down all infrastructure and remove networks
down:
	docker-compose -f $(COMPOSE_FILE) down

# Stop a specific service (e.g., make down-postgres)
down-%:
	docker-compose -f $(COMPOSE_FILE) stop $*

# Follow logs for all running containers
logs:
	docker-compose -f $(COMPOSE_FILE) logs -f

# Follow logs for a specific service (e.g., make logs-relay)
logs-%:
	docker-compose -f $(COMPOSE_FILE) logs -f $*

# Show status of all project containers
ps:
	docker-compose -f $(COMPOSE_FILE) ps

# ==========================================
# Tooling Setup
# ==========================================

# Install required local development tools and git hooks
setup:
	@echo "Installing tools and setting up pre-commit..."
	brew install pre-commit || pip install pre-commit
	pre-commit install
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/segmentio/golines@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# ==========================================
# NATS Management
# ==========================================

# Create the JetStream stream and bind the subject pattern
nats-setup:
	@chmod +x scripts/nats/setup-stream.sh
	./scripts/nats/setup-stream.sh $(NATS_URL) $(NATS_STREAM) "$(TOPIC_NAME)"

# View a historical list of messages currently in the JetStream
nats-view:
	docker-compose -f $(COMPOSE_FILE) exec nats-box \
		nats -s $(NATS_URL) stream view $(NATS_STREAM)

# Show detailed metadata, sequence numbers, and consumer counts for the stream
nats-info:
	docker-compose -f $(COMPOSE_FILE) exec nats-box \
		nats -s $(NATS_URL) stream info $(NATS_STREAM)

# ==========================================
# Kafka Management
# ==========================================

# Create the required Kafka topics with 3 partitions
kafka-setup:
	@chmod +x scripts/kafka/setup-topics.sh
	./scripts/kafka/setup-topics.sh $(KAFKA_URL) $(TOPIC_NAME) 3

# List all existing topics in the Kafka cluster
kafka-list:
	docker-compose -f $(COMPOSE_FILE) exec kafka \
		/opt/kafka/bin/kafka-topics.sh --list --bootstrap-server $(KAFKA_URL)

# Deep dive into the configuration and partition status of the topic
kafka-info:
	docker-compose -f $(COMPOSE_FILE) exec kafka \
		/opt/kafka/bin/kafka-topics.sh --describe \
		--topic $(TOPIC_NAME) \
		--bootstrap-server $(KAFKA_URL)

# Tail messages from the beginning of the topic in real-time
kafka-tail:
	docker-compose -f $(COMPOSE_FILE) exec kafka \
		/opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server $(KAFKA_URL) --topic $(TOPIC_NAME) --from-beginning

# ==========================================
# Observability
# ==========================================

# Send a batch of test traces to the OTel Collector to verify the pipeline
test-otel:
	@chmod +x scripts/otel/test-telemetry.sh
	./scripts/otel/test-telemetry.sh $(OTEL_ENDPOINT) $(OTEL_TRACE_COUNT)
