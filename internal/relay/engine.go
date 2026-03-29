package relay

import (
	"context"
	"log"
	"time"

	"go.uber.org/zap"
)

// Engine coordinates the movement of events from Storage to Publisher.
type Engine struct {
	storage   Storage
	publisher Publisher
	interval  time.Duration
	logger    *zap.Logger
}

// NewEngine creates a ready-to-run Relay Engine.
func NewEngine(s Storage, p Publisher, interval time.Duration, logger *zap.Logger) *Engine {
	return &Engine{
		storage:   s,
		publisher: p,
		interval:  interval,
		logger:    logger.With(zap.String("module", "engine")),
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
		e.logger.Error("failed to fetch events", zap.Error(err))
		return err // Could not connect to DB
	}

	for _, event := range events {
		// 2. Publish the event
		if err := e.publisher.Publish(ctx, event); err != nil {
			e.logger.Warn("publish failed", 
						zap.String("event_id", event.ID.String()),
						zap.String("topic", event.Topic),
						zap.Error(err),
			)
			// Instead of just 'continue', we tell the DB it failed
			_ = e.storage.MarkFailed(ctx, event.ID.String(), err.Error())
			continue 
		}

		e.logger.Info("event published", 
			zap.String("event_id", event.ID.String()),
			zap.Duration("elapsed", time.Since(event.CreatedAt)),
		)

		// 3. Mark as successfully processed
		if err := e.storage.MarkDone(ctx, event.ID.String()); err != nil {
			e.logger.Warn("mark as done failed", 
						zap.String("event_id", event.ID.String()),
						zap.String("topic", event.Topic),
						zap.Error(err),
			)
		}
	}

	return nil
}