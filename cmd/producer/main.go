package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	connStr := "postgres://postgres:postgres@localhost:5432/postgres"

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		log.Fatalf("Producer failed to connect: %v", err)
	}
	defer pool.Close()

	log.Println("Producer started! Inserting 1 event every second...")

	for {
		// 1. Create a fake event
		topic := "user.signup"
		payload := fmt.Sprintf(`{"user_id": %d, "email": "user-%d@example.com"}`, rand.Intn(10000), rand.Intn(10000))

		// 2. Insert into the Outbox table
		for i := 0; i < 100; i++{
			id := uuid.New()
			query := `INSERT INTO outbox_events (id, topic, payload, status) VALUES ($1, $2, $3, 'pending')`
			_, err := pool.Exec(ctx, query, id, topic, []byte(payload))
			
			if err != nil {
				log.Printf("Insert failed: %v", err)
			} else {
				log.Printf("Inserted Event: %s", id)
			}
		}
		
		// 3. Wait a bit before the next one
		time.Sleep(1 * time.Second)
	}
}