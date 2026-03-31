package relay

import (
	"context"
	"log"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// Engine coordinates the movement of events from Storage to Publisher.
type Engine struct {
	storage   Storage
	publisher Publisher
	interval  time.Duration
	batchSize int
	logger    *zap.Logger
	metrics   *Metrics
	tracer    oteltrace.Tracer
}

// NewEngine creates a ready-to-run Relay Engine.
func NewEngine(s Storage, p Publisher, i time.Duration, b int, l *zap.Logger, m *Metrics, t oteltrace.Tracer) *Engine {

	return &Engine{
		storage:   s,
		publisher: p,
		interval:  i,
		batchSize: b,
		logger:    l.With(zap.String("module", "engine")),
		metrics:   m,
		tracer:    t,
	}
}

// Run starts the polling loop. It stops when the context is cancelled.
func (e *Engine) Start(ctx context.Context) error {
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
	start := time.Now()
	ctx, span := e.tracer.Start(ctx, "Engine.ProcessBatch")
	defer span.End()
	// 1. Fetch a batch of events (we'll start with 10)
	events, err := e.storage.Fetch(ctx, e.batchSize)
	if err != nil && err != context.Canceled {
		span.RecordError(err)
		e.logger.Error("failed to fetch events", zap.Error(err))
		return err // Could not connect to DB
	}

	for _, event := range events {

		select {
		case <-ctx.Done():
			// Shutdown requested!
			// We stop processing the REST of the 1,000 events immediately.
			e.logger.Info("shutdown signal received, stopping batch mid-way",
				zap.Int("remaining", len(events)-100))
			return ctx.Err()
		default:
			// No shutdown? Carry on.
		}

		_, childSpan := e.tracer.Start(ctx, "Publisher.Publish",
			oteltrace.WithAttributes(
				attribute.String("event_id", event.ID.String()),
				attribute.String("type", event.Type),
			))

		res, err := e.publisher.Publish(ctx, event)
		//Temporary handling of the result
		e.logger.Info("Publish result", zap.String("Status", string(res.Status)))

		if err != nil {
			childSpan.RecordError(err)
			childSpan.SetStatus(codes.Error, "publish failed")
			childSpan.End() // End child
			e.logger.Warn("publish failed",
				zap.String("event_id", event.ID.String()),
				zap.String("type", event.Type),
				zap.Error(err),
			)
			e.metrics.Failed.Add(ctx, 1)
			// Instead of just 'continue', we tell the DB it failed
			_ = e.storage.MarkFailed(ctx, event.ID.String(), err.Error())
			continue
		}

		e.metrics.Delivered.Add(ctx, 1)
		e.logger.Info("event published",
			zap.String("event_id", event.ID.String()),
			zap.Duration("elapsed", time.Since(event.CreatedAt)),
		)

		// 3. Mark as successfully processed
		if err := e.storage.MarkDone(ctx, event.ID.String()); err != nil && err != context.Canceled {
			e.logger.Warn("mark as done failed",
				zap.String("event_id", event.ID.String()),
				zap.String("type", event.Type),
				zap.Error(err),
			)
		}
		childSpan.SetStatus(codes.Ok, "success")
		childSpan.End() // End child
		e.metrics.Latency.Record(ctx, time.Since(start).Seconds())
	}

	return nil
}
