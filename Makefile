.PHONY: run producer test clean

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

# Stop everything
down:
	docker-compose down