package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
)

// Simulating your DB Event structure
type DBEvent struct {
	ID           uuid.UUID
	Type         string
	PartitionKey string
	Payload      []byte
	Headers      json.RawMessage
}

func main() {
	topic := "event.user.signup"
	broker := "localhost:9092"
	totalMessages := 10000
	batchSize := 100

	writer := &kafka.Writer{
		Addr:         kafka.TCP(broker),
		Topic:        topic,
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireAll,
		Async:        false,
		Compression:  kafka.Snappy,
		BatchSize:    1, // Force immediate flush of our manual batch
	}
	defer writer.Close()

	// 1. Pre-generate "DB" data to simulate your Postgres rows
	dbRows := make([]DBEvent, totalMessages)
	for i := 0; i < totalMessages; i++ {
		dbRows[i] = DBEvent{
			ID:           uuid.New(),
			Type:         "event.user.signup",
			PartitionKey: fmt.Sprintf("user-%d", i%100),
			Payload:      []byte("binary data payload simulation"),
			Headers:      json.RawMessage(`{"source": "load-test-script", "trace_id": "` + uuid.New().String() + `"}`),
		}
	}

	fmt.Printf("🚀 Starting Realistic Benchmark: %d messages, Batch: %d\n", totalMessages, batchSize)
	start := time.Now()

	for i := 0; i < totalMessages; i += batchSize {
		// This slice simulates your 'msgs := make([]kafka.Message, 0, len(events))'
		kafkaBatch := make([]kafka.Message, 0, batchSize)

		for j := 0; j < batchSize; j++ {
			row := dbRows[i+j]

			// --- SIMULATE YOUR mapToKafkaMessage LOGIC ---
			// 1. Unmarshal JSON Headers (The expensive part!)
			var userHeaders map[string]string
			_ = json.Unmarshal(row.Headers, &userHeaders)

			// 2. Convert to Kafka Headers
			kHeaders := make([]kafka.Header, 0, len(userHeaders)+1)
			for k, v := range userHeaders {
				kHeaders = append(kHeaders, kafka.Header{Key: k, Value: []byte(v)})
			}
			kHeaders = append(kHeaders, kafka.Header{Key: "X-Event-ID", Value: []byte(row.ID.String())})

			// 3. Build Message
			kafkaBatch = append(kafkaBatch, kafka.Message{
				Key:     []byte(row.PartitionKey),
				Value:   row.Payload,
				Headers: kHeaders,
			})
		}

		// Send the batch
		err := writer.WriteMessages(context.Background(), kafkaBatch...)
		if err != nil {
			log.Fatalf("❌ Batch failed: %v", err)
		} else {
			fmt.Printf("\n--- Sent batch %d ---\n", i)
		}
	}

	duration := time.Since(start)
	fmt.Printf("\n--- Results ---\n")
	fmt.Printf("Total Time: %v\n", duration)
	fmt.Printf("Throughput: %.2f events/sec\n", float64(totalMessages)/duration.Seconds())
}
