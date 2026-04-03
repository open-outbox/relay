package relay

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

const (
	instrumentationName  = "github.com/open-outbox/relay/engine"
	defaultWatchInterval = 30 * time.Second
)

// Engine coordinates the movement of events from Storage to Publisher.
type Engine struct {
	relayId       string
	storage       Storage
	publisher     Publisher
	interval      time.Duration
	leaseTimeout  time.Duration
	reapBatchSize int
	batchSize     int
	policy        RetryPolicy
	logger        *zap.Logger
	metrics       *Metrics
	tracer        trace.Tracer
	meter         metric.Meter
}

// NewEngine creates a ready-to-run Relay Engine.
func NewEngine(
	storage Storage,
	publisher Publisher,
	interval time.Duration,
	batchSize int,
	leaseTimeout time.Duration,
	reapBatchSize int,
	logger *zap.Logger,
	metrics *Metrics,
	traceProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
) *Engine {

	return &Engine{
		relayId:       generateRelayID(),
		storage:       storage,
		publisher:     publisher,
		interval:      interval,
		leaseTimeout:  leaseTimeout,
		reapBatchSize: reapBatchSize,
		batchSize:     batchSize,
		logger:        logger.With(zap.String("module", "engine")),
		metrics:       metrics,
		tracer:        traceProvider.Tracer(instrumentationName),
		meter:         meterProvider.Meter(instrumentationName),
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

	go e.watchBacklog(ctx)
	go e.ReapExpiredLeases(ctx)

	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			count, err := e.process(ctx)
			if err != nil {
				// On error, wait a bit so we don't spam a failing DB/Broker
				e.logger.Error("Process error", zap.Error(err))
				time.Sleep(e.interval)
				continue
			}

			// GUARD: If the pond isn't full, take a break.
			if count < e.batchSize {
				time.Sleep(e.interval)
				continue
			}
		}
	}
}

func (e *Engine) watchBacklog(ctx context.Context) error {
	ticker := time.NewTicker(defaultWatchInterval)
	defer ticker.Stop()

	e.updateBacklogMetrics(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			e.updateBacklogMetrics(ctx)
		}
	}
}

func (e *Engine) ReapExpiredLeases(ctx context.Context) error {

	ticker := time.NewTicker(e.leaseTimeout)
	defer ticker.Stop()

	_, err := e.storage.ReapExpiredLeases(ctx, e.leaseTimeout, e.reapBatchSize)

	if err != nil && err != context.Canceled {
		e.logger.Error("failed to fetch events.", zap.Error(err))
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_, err := e.storage.ReapExpiredLeases(ctx, e.leaseTimeout, e.reapBatchSize)
			if err != nil && err != context.Canceled {
				e.logger.Error("failed to fetch events.", zap.Error(err))
			}
		}
	}
}

func (e *Engine) process(ctx context.Context) (int, error) {

	e.logger.Debug("Engine processing...")

	// Start the Parent Span for the entire batch
	ctx, span := e.tracer.Start(ctx, "Engine.Process",
		trace.WithAttributes(attribute.Int("batch.size_requested", e.batchSize)))
	defer span.End()

	claimStart := time.Now()
	// Claim a batch of events
	events, err := e.storage.ClaimBatch(ctx, e.relayId, e.batchSize)
	if err != nil && err != context.Canceled {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.logger.Error("failed to fetch events.", zap.Error(err))
		return 0, err
	}
	e.metrics.StorageLatency.Record(ctx, time.Since(claimStart).Seconds(),
		metric.WithAttributes(attribute.String("op", "claim")))
	e.metrics.BatchSize.Record(ctx, int64(len(events)))

	// Record number of claimed events
	span.SetAttributes(attribute.Int("batch.size_actual", len(events)))

	successEvents := make([]uuid.UUID, 0)
	failedEvents := make([]FailedEvent, 0)

	for _, event := range events {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}

		// Create a new Context from the child span
		// This allows the Publisher (Kafka/NATS) to attach its own spans to this one.
		publishCtx, childSpan := e.tracer.Start(ctx, "Publisher.Publish",
			trace.WithAttributes(
				attribute.String("event.id", event.ID.String()),
				attribute.String("event.type", event.Type),
				attribute.Int("event.attempt", event.Attempts),
			))

		// Publish the event
		publishStart := time.Now()

		res, err := e.publisher.Publish(publishCtx, event)

		e.metrics.PublisherLatency.Record(ctx, time.Since(publishStart).Seconds(),
			metric.WithAttributes(attribute.String("type", event.Type)))

		if err != nil && err != context.Canceled {
			// Add event to failed events
			failedEvents = append(failedEvents, e.assessFailure(event, err))

			e.metrics.EventsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("status", "failed")))

			childSpan.RecordError(err)
			childSpan.SetStatus(codes.Error, err.Error())
			childSpan.End()
			e.logger.Warn("publish failed",
				zap.String("event_id", event.ID.String()),
				zap.String("type", event.Type),
				zap.Error(err),
			)

			continue
		}

		successEvents = append(successEvents, event.ID)

		e.metrics.EndToEndLatency.Record(ctx, time.Since(event.CreatedAt).Seconds(),
			metric.WithAttributes(attribute.String("type", event.Type)))
		e.metrics.EventsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("status", "success")))

		// Success Telemetry
		childSpan.SetStatus(codes.Ok, "success")
		childSpan.SetAttributes(attribute.String("provider.id", res.ProviderID))
		childSpan.SetAttributes(attribute.String("publish.duration", time.Since(publishStart).String()))
		childSpan.End()

		e.logger.Info("event published",
			zap.String("event_id", event.ID.String()),
			zap.Duration("publish_duration", time.Since(publishStart)),
		)
	}

	if len(successEvents) > 0 {
		_, finalizeSpan := e.tracer.Start(ctx, "Storage.MarkDeliveredBatch")
		finalizeSpan.SetAttributes(attribute.Int("batch.size", len(successEvents)))

		finalizeStart := time.Now()
		err := e.storage.MarkDeliveredBatch(ctx, successEvents, e.relayId)

		status := "success"
		if err != nil {
			status = "error"
		}
		e.metrics.StorageLatency.Record(ctx, time.Since(finalizeStart).Seconds(),
			metric.WithAttributes(
				attribute.String("op", "mark_delivered"),
				attribute.String("status", status),
			),
		)

		if err != nil && err != context.Canceled {
			finalizeSpan.RecordError(err)
			finalizeSpan.SetStatus(codes.Error, err.Error())
			finalizeSpan.End()
			e.logger.Error("failed to mark batch as delivered", zap.Error(err))
			return 0, err
		}
		finalizeSpan.End()

	}

	if len(failedEvents) > 0 {
		_, failSpan := e.tracer.Start(ctx, "Storage.MarkFailedBatch")
		failSpan.SetAttributes(attribute.Int("batch.size", len(failedEvents)))

		failStart := time.Now()
		err := e.storage.MarkFailedBatch(ctx, failedEvents, e.relayId)

		// Record Database Latency for the Failure Update
		status := "success"
		if err != nil {
			status = "error"
		}
		e.metrics.StorageLatency.Record(ctx, time.Since(failStart).Seconds(),
			metric.WithAttributes(
				attribute.String("op", "mark_failed"),
				attribute.String("status", status),
			),
		)

		if err != nil && err != context.Canceled {
			failSpan.RecordError(err)
			failSpan.SetStatus(codes.Error, err.Error())
			failSpan.End()
			e.logger.Error("failed to mark failure batch", zap.Error(err))
			return 0, err
		}
		failSpan.End()
	}

	return len(events), nil
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

func (e *Engine) updateBacklogMetrics(ctx context.Context) {

	stats, err := e.storage.GetStats(ctx)
	if err != nil && err != context.Canceled {

		e.logger.Warn("telemetry: failed to retrieve backlog stats",
			zap.Error(err),
			zap.String("relay_id", e.relayId),
		)
		return
	}

	e.metrics.PendingGauge.Record(ctx, stats.PendingCount)
	e.metrics.OldestPendingSeconds.Record(ctx, stats.OldestAgeSec)
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

func generateRelayID() string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown-relay"
	}

	// Add a short random suffix (e.g., relay-worker-6f2d)
	// to prevent collisions if a Pod restarts quickly and the DB
	// hasn't cleared the old "DELIVERING" rows yet.
	suffix := uuid.New().String()[:4]

	return fmt.Sprintf("%s-%s", hostname, suffix)
}
