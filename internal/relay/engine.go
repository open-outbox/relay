package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

const (
	defaultWatchInterval = 30 * time.Second
)

// Engine coordinates the movement of events from Storage to Publisher.
type Engine struct {
	relayId            string
	storage            Storage
	publisher          Publisher
	interval           time.Duration
	leaseTimeout       time.Duration
	reapBatchSize      int
	batchSize          int
	enableBatchPublish bool
	policy             RetryPolicy
	logger             *zap.Logger
	metrics            *telemetry.Metrics
	tracer             trace.Tracer
	meter              metric.Meter
	events             []Event
}

// EngineParams handles the tuning and identity.
type EngineParams struct {
	RelayID            string
	Interval           time.Duration
	BatchSize          int
	LeaseTimeout       time.Duration
	ReapBatchSize      int
	RetryPolicy        RetryPolicy
	EnableBatchPublish bool
}

// NewEngine creates a ready-to-run Relay Engine.
func NewEngine(
	storage Storage,
	publisher Publisher,
	params EngineParams,
	tel telemetry.Telemetry,
) *Engine {
	// If RelayID is empty, generate one as a fallback
	id := params.RelayID
	if id == "" {
		id = generateRelayID()
	}

	return &Engine{
		relayId:            id,
		storage:            storage,
		publisher:          publisher,
		interval:           params.Interval,
		batchSize:          params.BatchSize,
		leaseTimeout:       params.LeaseTimeout,
		reapBatchSize:      params.ReapBatchSize,
		enableBatchPublish: params.EnableBatchPublish,
		policy:             params.RetryPolicy,
		logger:             tel.ScopedLogger("engine"),
		metrics:            tel.Metrics,
		tracer:             tel.Tracer,
		meter:              tel.Meter,
		events:             make([]Event, params.BatchSize),
	}
}

// NewEngine creates a ready-to-run Relay Engine.

// Run starts the polling loop. It stops when the context is cancelled.
func (e *Engine) Start(ctx context.Context) error {
	// Create an errgroup derived from the parent context.
	// If any goroutine returns an error, 'gCtx' is cancelled.
	g, gCtx := errgroup.WithContext(ctx)
	// 1. Background: Metrics Watcher
	g.Go(func() error {
		return e.watchBacklog(gCtx)
	})

	// 2. Background: Lease Reaper (Self-healing)
	g.Go(func() error {
		return e.reapExpiredLeases(gCtx)
	})

	// 3. Main Loop: Event Processing
	g.Go(func() error {
		ticker := time.NewTicker(e.interval)
		defer ticker.Stop()

		for {
			select {
			case <-gCtx.Done():
				return gCtx.Err()
			case <-ticker.C:
				count, err := e.process(gCtx)
				if err != nil {
					// We log and wait; the errgroup will catch critical errors if 'process' returns them.
					e.LogIfError(err, "Batch processing failed")
					time.Sleep(e.interval)
					continue
				}

				// High-throughput optimization: if the batch was full, don't wait for the ticker.
				if count >= e.batchSize {
					continue
				}
			}
		}
	})

	e.logger.Info("Engine started", zap.String("relay_id", e.relayId))

	// Wait for all goroutines to finish or for the first one to fail.
	return g.Wait()

}

// Stop handles the graceful cleanup of the Engine's dependencies.
func (e *Engine) Stop() error {
	e.logger.Info("Stopping engine: shutting down storage and publisher...")

	var errs []error

	// We close storage first to stop picking up new work
	if err := e.storage.Close(); err != nil {
		errs = append(errs, fmt.Errorf("storage close: %w", err))
	}

	if err := e.publisher.Close(); err != nil {
		errs = append(errs, fmt.Errorf("publisher close: %w", err))
	}

	if len(errs) > 0 {
		// join errors if using Go 1.20+
		return errors.Join(errs...)
	}

	return nil
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

func (e *Engine) reapExpiredLeases(ctx context.Context) error {

	ticker := time.NewTicker(e.leaseTimeout)
	defer ticker.Stop()

	_, err := e.storage.ReapExpiredLeases(ctx, e.leaseTimeout, e.reapBatchSize)

	e.LogIfError(err, "failed to fetch events.", zap.Error(err))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_, err := e.storage.ReapExpiredLeases(ctx, e.leaseTimeout, e.reapBatchSize)
			e.LogIfError(err, "failed to fetch events.", zap.Error(err))
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
	events, err := e.storage.ClaimBatch(ctx, e.relayId, e.batchSize, e.events)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.LogIfError(err, "failed to fetch events.", zap.Error(err))
		return 0, err
	}

	e.metrics.StorageLatency.Record(ctx, time.Since(claimStart).Seconds(),
		metric.WithAttributes(attribute.String("op", "claim")))
	e.metrics.BatchSize.Record(ctx, int64(len(events)))

	// Record number of claimed events
	span.SetAttributes(attribute.Int("batch.size_actual", len(events)))

	var successEvents []uuid.UUID
	var failedEvents []FailedEvent

	// if e.enableBatchPublish {
	// successEvents, failedEvents, err = e.publishBatch(ctx, events)
	// } else {
	successEvents, failedEvents, err = e.publishOnByOne(ctx, events)
	// }

	if err != nil {
		return 0, err
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

		if err != nil {
			finalizeSpan.RecordError(err)
			finalizeSpan.SetStatus(codes.Error, err.Error())
			e.LogIfError(err, "failed to mark batch as delivered", zap.Error(err))
			finalizeSpan.End()
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

		if err != nil {
			failSpan.RecordError(err)
			failSpan.SetStatus(codes.Error, err.Error())
			failSpan.End()
			e.LogIfError(err, "failed to mark failure batch", zap.Error(err))
			return 0, err
		}
		failSpan.End()
	}

	if len(events) > 0 {
		e.logger.Info("batch completed",
			zap.Int("count", len(events)),
			zap.Int("successful", len(successEvents)),
			zap.Int("failed", len(failedEvents)),
		)
	}

	return len(events), nil
}

func (e *Engine) publishOnByOne(
	ctx context.Context,
	events []Event,
) ([]uuid.UUID, []FailedEvent, error) {

	successEvents := make([]uuid.UUID, 0, len(events))
	failedEvents := make([]FailedEvent, 0, len(events))

	for _, event := range events {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}

		err := e.publisher.Publish(ctx, event)

		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, nil, err
			}

			failedEvents = append(failedEvents, e.assessFailure(event, err))
			e.metrics.EventsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("status", "failed")))

			// We return the error or continue the loop based on your retry logic
			continue
		}

		successEvents = append(successEvents, event.ID)

		e.metrics.EndToEndLatency.Record(ctx, time.Since(event.CreatedAt).Seconds(),
			metric.WithAttributes(attribute.String("type", event.Type)))
		e.metrics.EventsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("status", "success")))

		e.logger.Debug("event published",
			zap.String("event_id", event.ID.String()),
			zap.String("type", event.Type),
		)
	}

	return successEvents, failedEvents, nil
}

func (e *Engine) publishBatch(
	ctx context.Context,
	events []Event,
) ([]uuid.UUID, []FailedEvent, error) {

	// err := e.publisher.PublishBatch(ctx, events)
	err := fmt.Errorf("Batch publishing is not enabled yet")
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, nil, err
		}

		failures := make([]FailedEvent, 0, len(events))
		for _, ev := range events {
			failures = append(failures, e.assessFailure(ev, err))
		}

		e.metrics.EventsTotal.Add(ctx, int64(len(events)),
			metric.WithAttributes(attribute.String("status", "failed")))

		return nil, failures, nil
	}

	successIDs := make([]uuid.UUID, 0, len(events))
	for _, ev := range events {
		successIDs = append(successIDs, ev.ID)
		e.metrics.EndToEndLatency.Record(ctx, time.Since(ev.CreatedAt).Seconds(),
			metric.WithAttributes(attribute.String("type", ev.Type)))
	}

	e.metrics.EventsTotal.Add(ctx, int64(len(events)),
		metric.WithAttributes(attribute.String("status", "success")))

	return successIDs, nil, nil
}

func (e *Engine) assessFailure(event Event, publishError error) FailedEvent {
	nextAttempts := event.Attempts + 1
	delay, policyAllowsRetry := e.policy.NextBackoff(nextAttempts)

	isRetryable := true
	var pErr *PublishError
	if errors.As(publishError, &pErr) {
		isRetryable = pErr.IsRetryable
	}

	// Final decision: Policy must allow it AND Error must be retryable
	shouldRetry := policyAllowsRetry && isRetryable

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
		result.AvailableAt = time.Now()

		// Event is not processable by the publisher
		if !isRetryable {
			e.logger.Warn("event killed: non-retryable error",
				zap.String("event_id", event.ID.String()),
				zap.String("type", event.Type),
				zap.Error(publishError),
			)
		}
	}

	return result
}

func (e *Engine) updateBacklogMetrics(ctx context.Context) {

	stats, err := e.storage.GetStats(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			e.logger.Warn("telemetry: failed to retrieve backlog stats",
				zap.Error(err),
				zap.String("relay_id", e.relayId),
			)
		}
		return
	}

	e.metrics.PendingGauge.Record(ctx, stats.PendingCount)
	e.metrics.OldestPendingSeconds.Record(ctx, stats.OldestAgeSec)
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

func (e *Engine) LogIfError(err error, msg string, fields ...zap.Field) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	e.logger.Error(msg, fields...)
}
