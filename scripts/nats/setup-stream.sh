#!/bin/bash

# Arguments with defaults
NATS_SERVER=${1:-"nats:4222"}
STREAM_NAME=${2:-"OUTBOX_EVENTS"}
SUBJECT_PATTERN=${3:-"outbox.events.v1"}

echo "Configuring NATS JetStream: $STREAM_NAME..."
echo "Listening on subjects: $SUBJECT_PATTERN"

# Create the stream using the dynamic subject pattern
docker-compose -f deployments/infra-docker-compose.yaml exec nats-box \
  nats stream add "$STREAM_NAME" \
  --server "$NATS_SERVER" \
  --subjects "$SUBJECT_PATTERN" \
  --storage file \
  --retention limits \
  --max-msgs=-1 \
  --max-bytes=-1 \
  --discard old \
  --dupe-window 2m \
  --replicas 1 \
  --defaults

echo "Stream '$STREAM_NAME' is ready."
