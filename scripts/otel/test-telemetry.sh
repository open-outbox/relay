#!/bin/bash

# Configuration - Match your .env OTel settings
ENDPOINT=${1:-"localhost:4317"}
TRACE_COUNT=${2:-1}

echo "Sending $TRACE_COUNT test traces to $ENDPOINT..."

# Check if telemetrygen is installed
if ! command -v telemetrygen &> /dev/null; then
    echo "Error: telemetrygen is not installed."
    echo "Run: go install github.com/open-telemetry/opentelemetry-collector-contrib/cmd/telemetrygen@latest"
    exit 1
fi

# Execute the test
telemetrygen traces \
  --otlp-endpoint "$ENDPOINT" \
  --otlp-insecure \
  --traces "$TRACE_COUNT" \
  --rate 10

echo "Done. Check Jaeger/Zipkin (via OTel Collector) to verify receipt."