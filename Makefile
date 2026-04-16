# --- Load environment variables from .env if it exists ---
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

# --- Configuration & Defaults ---
BINARY_NAME         := relay
CLI_BINARY_NAME		:= cli
COMPOSE_FILE        := deployments/infra-docker-compose.yaml
MAIN_PACKAGE        := ./cmd/relay/main.go
CLI_PACKAGE         := ./cmd/cli
PRODUCER_PKG        := ./cmd/producer/main.go
KAFKA_URL           := kafka:9092
NATS_URL            := nats:4222
OTEL_ENDPOINT       := localhost:4317

TOPIC_NAME          := $(LOCAL_TEST_TOPIC)
NATS_STREAM         := $(LOCAL_NATS_STREAM)
OTEL_TRACE_COUNT    := $(LOCAL_OTEL_TEST_TRACE_COUNT)
DB_TYPE             := $(STORAGE_TYPE)

.PHONY: all build run produce test clean fmt lint up down setup help ps logs docs-dev

.DEFAULT_GOAL := help

help: ## Display this help message
	@grep -h -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ==========================================
# Development & Execution
# ==========================================

run: ## Run the Relay service locally using Go
	go run $(MAIN_PACKAGE)

produce: ## Run the Producer to generate dummy events for testing
	go run $(PRODUCER_PKG)

build: ## Compile the Relay into a binary in the bin/ directory
	mkdir -p bin
	go build -o bin/$(BINARY_NAME) $(MAIN_PACKAGE)
	go build -o bin/$(CLI_BINARY_NAME) $(CLI_PACKAGE)

clean: ## Remove build binaries and clear Go test cache
	rm -rf bin/
	go clean -testcache

docker-build: ## Builds the production-ready OCI container image.
	docker build -f deployments/Dockerfile -t openoutbox-relay:v1.0 .

docs-dev: ## Runs the documentation website in dev mode.
	cd docs && npm install && npm run dev

gen-api:
	gomarkdoc \
  		--template-file file=docs/starlight-file.gotxt \
  		--template-file package=docs/starlight-package.gotxt \
  		--output 'docs/src/content/docs/reference/api/{{.ImportPath}}.md' \
  		./...
	find docs/src/content/docs/reference/api -name "*.md" -exec sh -c 'mv "$$1" "$${1%.md}.mdx"' _ {} \;


# ==========================================
# Quality & Linting
# ==========================================

fmt: ## Format code, organize imports, and enforce 100-char line limits
	goimports -w .
	golines . -w --max-len=100
	go mod tidy

lint: fmt ## Run golangci-lint to catch code quality issues
	golangci-lint run ./...

test: ## Run all project tests with the race detector enabled
	go test -v -race ./...

# ==========================================
# Infrastructure (Docker)
# ==========================================

up: ## Spin up all infrastructure (Postgres, Kafka, NATS, OTel)
	docker-compose -f $(COMPOSE_FILE) up -d

up-%: ## Spin up a specific service (e.g., make up-kafka)
	docker-compose -f $(COMPOSE_FILE) up -d $*

down: ## Shut down all infrastructure and remove networks
	docker-compose -f $(COMPOSE_FILE) down

down-%: ## Stop a specific service (e.g., make down-postgres)
	docker-compose -f $(COMPOSE_FILE) stop $*

logs: ## Follow logs for all running containers
	docker-compose -f $(COMPOSE_FILE) logs -f

logs-%: ## Follow logs for a specific service (e.g., make logs-relay)
	docker-compose -f $(COMPOSE_FILE) logs -f $*

ps: ## Show status of all project containers
	docker-compose -f $(COMPOSE_FILE) ps

# ==========================================
# Tooling Setup
# ==========================================

setup: ## Install required local development tools and git hooks
	@echo "Installing tools and setting up pre-commit..."
	brew install pre-commit || pip install pre-commit
	pre-commit install
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/segmentio/golines@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@latest

# ==========================================
# NATS Management
# ==========================================

nats-setup: ## Create the JetStream stream and bind the subject pattern
	@chmod +x scripts/nats/setup-stream.sh
	./scripts/nats/setup-stream.sh $(NATS_URL) $(NATS_STREAM) "$(TOPIC_NAME)"

nats-view: ## View messages currently in the JetStream
	docker-compose -f $(COMPOSE_FILE) exec nats-box nats -s $(NATS_URL) stream view $(NATS_STREAM)

nats-info: ## Show detailed metadata and sequence numbers for the stream
	docker-compose -f $(COMPOSE_FILE) exec nats-box nats -s $(NATS_URL) stream info $(NATS_STREAM)

# ==========================================
# Kafka Management
# ==========================================

kafka-setup: ## Create the required Kafka topics with 3 partitions
	@chmod +x scripts/kafka/setup-topics.sh
	./scripts/kafka/setup-topics.sh $(KAFKA_URL) $(TOPIC_NAME) 3

kafka-list: ## List all existing topics in the Kafka cluster
	docker-compose -f $(COMPOSE_FILE) exec kafka /opt/kafka/bin/kafka-topics.sh --list --bootstrap-server $(KAFKA_URL)

kafka-info: ## Deep dive into the configuration of the topic
	docker-compose -f $(COMPOSE_FILE) exec kafka \
	/opt/kafka/bin/kafka-topics.sh --describe \
	--topic $(TOPIC_NAME) \
	--bootstrap-server $(KAFKA_URL)

kafka-tail: ## Tail messages from the beginning of the topic in real-time
	docker-compose -f $(COMPOSE_FILE) exec kafka \
	/opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server $(KAFKA_URL) --topic $(TOPIC_NAME) --from-beginning

# ==========================================
# Observability & Database
# ==========================================

test-otel: ## Send a batch of test traces to verify the OTel pipeline
	@chmod +x scripts/otel/test-telemetry.sh
	./scripts/otel/test-telemetry.sh $(OTEL_ENDPOINT) $(OTEL_TRACE_COUNT)

db-init: ## Detects STORAGE_TYPE and applies the correct SQL schema
	@echo "Initializing $(DB_TYPE) schema..."
ifeq ($(DB_TYPE),postgres)
	docker-compose -f $(COMPOSE_FILE) exec -T postgres psql -U postgres -d postgres < schema/postgres/openoutbox.sql
else
	@echo "Error: Unknown STORAGE_TYPE '$(DB_TYPE)'. Please check your .env"
	@exit 1
endif
	@echo "$(DB_TYPE) schema applied."
