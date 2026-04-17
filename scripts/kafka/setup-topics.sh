#!/bin/bash

# Configuration
BOOTSTRAP_SERVER=${1:-"kafka:9092"}
TOPIC_NAME=${2:-"outbox.events.v1"}
PARTITIONS=${3:-3}

echo "Configuring Kafka Topic: $TOPIC_NAME..."

# Check if topic exists
EXISTING=$(docker-compose -f deployments/infra-docker-compose.yaml exec kafka \
  /opt/kafka/bin/kafka-topics.sh --list --bootstrap-server "$BOOTSTRAP_SERVER" | grep -w "$TOPIC_NAME")

if [ "$EXISTING" == "$TOPIC_NAME" ]; then
    echo "Topic '$TOPIC_NAME' already exists. Skipping creation."
else
    docker-compose -f deployments/infra-docker-compose.yaml exec kafka \
      /opt/kafka/bin/kafka-topics.sh --create \
      --bootstrap-server "$BOOTSTRAP_SERVER" \
      --topic "$TOPIC_NAME" \
      --partitions "$PARTITIONS" \
      --replication-factor 1 \
      --config min.insync.replicas=1 \
      --config cleanup.policy=delete \
      --config retention.ms=604800000
    echo "Topic '$TOPIC_NAME' created with $PARTITIONS partitions."
fi
