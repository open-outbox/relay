.PHONY: run producer test clean fmt lint up down setup docs

# Start the infrastructure
up:
	docker-compose up -d

# Run the Relay
run:
	DATABASE_URL="postgres://user:password@localhost:5432/outbox_db" \
	NATS_URL="nats://localhost:4222" \
	go run cmd/relay/main.go

# Run the Producer to generate traffic
producer:
	go run cmd/producer/main.go

# Format code and tidy modules
fmt:
	goimports -w .
	golines . -w --max-len=100
	go mod tidy

# Run linters
lint: fmt
	golangci-lint run ./...

# Docs
docs: 
	pkgsite -open .

# Install development dependencies
setup:
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Stop everything
down:
	docker-compose down