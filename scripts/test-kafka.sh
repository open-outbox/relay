docker compose exec kafka /opt/kafka/bin/kafka-topics.sh \
  --create \
  --bootstrap-server localhost:9092 \
  --topic your.event.name \
  --partitions 3 \
  --replication-factor 1 \
  --config min.insync.replicas=1 \
  --config cleanup.policy=delete \
  --config retention.ms=604800000




# Deep dive into a specific topic
docker compose exec kafka /opt/kafka/bin/kafka-topics.sh \
  --describe \
  --topic your.event.name \
  --bootstrap-server localhost:9092
