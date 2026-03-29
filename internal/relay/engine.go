package relay

import (
	"context"
	"log"
	"time"
)

// Engine coordinates the movement of events from Storage to Publisher.
type Engine struct {
	storage   Storage
	publisher Publisher
	interval  time.Duration
}

// NewEngine creates a ready-to-run Relay Engine.
func NewEngine(s Storage, p Publisher, interval time.Duration) *Engine {
	return &Engine{
		storage:   s,
		publisher: p,
		interval:  interval,
	}
}

// Run starts the polling loop. It stops when the context is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := e.process(ctx); err != nil {
				log.Printf("Process error: %v", err)
			}
		}
	}
}

func (e *Engine) process(ctx context.Context) error {
	// 1. Fetch a batch of events (we'll start with 10)
	events, err := e.storage.Fetch(ctx, 10)
	if err != nil {
		return err // Could not connect to DB
	}

	for _, event := range events {
		// 2. Publish the event
		if err := e.publisher.Publish(ctx, event.Topic, event.Payload); err != nil {
        log.Printf("Failed to publish %s: %v", event.ID, err)
        // Instead of just 'continue', we tell the DB it failed
        _ = e.storage.MarkFailed(ctx, event.ID.String(), err.Error())
        continue 
    }

		// 3. Mark as successfully processed
		if err := e.storage.MarkDone(ctx, event.ID.String()); err != nil {
			log.Printf("Failed to mark event %s as done: %v", event.ID, err)
		}
	}

	return nil
}