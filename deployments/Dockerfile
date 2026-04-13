# --- Stage 1: Build ---
# Using the official Go image (which is Debian-based)
FROM --platform=$BUILDPLATFORM golang:1.26.1-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH

# Install CA certificates for secure connections (NATS/Postgres TLS)
RUN apt-get update && apt-get install -y ca-certificates --no-install-recommends

WORKDIR /app

# 1. Copy dependency files to leverage Docker layer caching
COPY go.mod go.sum ./
RUN go mod download

# 2. Copy the rest of the source code
COPY . .

# 3. Build the binary
# We still use CGO_ENABLED=0 for maximum portability,
# but it will link perfectly against Debian's libraries if needed.
RUN CGO_ENABLED=0 $TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o relay ./cmd/relay/main.go
RUN CGO_ENABLED=0 $TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o cli ./cmd/cli

# --- Stage 2: Final Runtime ---
# Using Debian Slim for the production environment
FROM debian:bookworm-slim

# Install CA certificates (Required for HTTPS/TLS)
RUN apt-get update && \
    apt-get install -y ca-certificates && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /root/

# Copy the binary from the builder
COPY --from=builder /app/relay .
COPY --from=builder /app/cli .

# Expose the Health/Metrics API port
EXPOSE 8080

# Run as a non-root user (Veteran Security Move)
# This prevents an attacker from gaining root access if they break the app
RUN useradd -ms /bin/bash openoutbox
USER openoutbox

CMD ["./relay"]
