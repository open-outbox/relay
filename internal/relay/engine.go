package relay

import (
	"context"
	"log"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

const instrumentationName = "github.com/open-outbox/relay/engine"

// Engine coordinates the movement of events from Storage to Publisher.
type Engine struct {
	relayId      string
	storage      Storage
	publisher    Publisher
	interval     time.Duration
	leaseMinutes int
	batchSize    int
	policy       RetryPolicy
	logger       *zap.Logger
	metrics      *Metrics
	tracer       trace.Tracer
	meter        metric.Meter
}

// NewEngine creates a ready-to-run Relay Engine.
func NewEngine(
	storage Storage,
	publisher Publisher,
	interval time.Duration,
	batchSize int,
	leaseMinutes int,
	logger *zap.Logger,
	metrics *Metrics,
	traceProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
) *Engine {

	return &Engine{
		relayId:      "change latter",
		storage:      storage,
		publisher:    publisher,
		interval:     interval,
		leaseMinutes: leaseMinutes,
		batchSize:    batchSize,
		logger:       logger.With(zap.String("module", "engine")),
		metrics:      metrics,
		tracer:       traceProvider.Tracer(instrumentationName),
		meter:        meterProvider.Meter(instrumentationName),
		policy: RetryPolicy{
			MaxAttempts: 10,
			BaseDelay:   1 * time.Second,
			MaxDelay:    24 * time.Hour,
		},
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
	events, err := e.storage.ClaimBatch(ctx, e.relayId, e.batchSize, e.leaseMinutes)
	if err != nil && err != context.Canceled {
		span.RecordError(err)
		e.logger.Error("failed to fetch events", zap.Error(err))
		return err // Could not connect to DB
	}

	successEvents := make([]uuid.UUID, 0)
	failedEvents := make([]FailedEvent, 0)

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
			trace.WithAttributes(
				attribute.String("event_id", event.ID.String()),
				attribute.String("type", event.Type),
			))

		res, err := e.publisher.Publish(ctx, event)
		//Temporary handling of the result
		e.logger.Info("Publish result", zap.String("Status", string(rune(res.Status))))

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
			failedEvents = append(failedEvents, e.assessFailure(event, err))
			continue
		}

		e.metrics.Delivered.Add(ctx, 1)
		e.logger.Info("event published",
			zap.String("event_id", event.ID.String()),
			zap.Duration("elapsed", time.Since(event.CreatedAt)),
		)

		// 3. Mark as successfully processed
		successEvents = append(successEvents, event.ID)
		childSpan.SetStatus(codes.Ok, "success")
		childSpan.End() // End child
		e.metrics.Latency.Record(ctx, time.Since(start).Seconds())
	}

	if len(successEvents) > 0 {
		if err := e.storage.MarkDeliveredBatch(ctx, successEvents, e.relayId); err != nil &&
			err != context.Canceled {
			e.logger.Error("failed to mark success batch", zap.Error(err))
			return err
		}
	}

	if len(failedEvents) > 0 {
		if err := e.storage.MarkFailedBatch(ctx, failedEvents, e.relayId); err != nil &&
			err != context.Canceled {
			e.logger.Error("failed to mark failure batch", zap.Error(err))
			return err
		}
	}

	return nil
}

func (e *Engine) assessFailure(event Event, publishError error) FailedEvent {
	nextAttempts := event.Attempts + 1
	delay, shouldRetry := e.policy.NextBackoff(nextAttempts)

	result := FailedEvent{
		ID:        event.ID,
		Attempts:  nextAttempts,
		LastError: publishError.Error(),
	}

	if shouldRetry {
		result.NewStatus = StatusPending
		result.AvailableAt = time.Now().Add(delay)
	} else {
		result.NewStatus = StatusDead
	}

	return result
}

type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

func (p RetryPolicy) NextBackoff(attempts int) (time.Duration, bool) {
	if attempts >= p.MaxAttempts {
		return 0, false
	}

	// Exponential math: 2^(attempts-1)
	delay := p.BaseDelay * time.Duration(1<<(uint(attempts-1)))

	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}

	//add a 10% random variation to prevent Thundering Herd
	jitter := time.Duration(rand.Int63n(int64(delay / 10)))

	return delay + jitter, true
}
