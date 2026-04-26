package relay

import (
	"context"
	"errors"
	"fmt"
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
// It manages the polling loop, background maintenance tasks like lease reaping,
// and ensures that events are processed according to the configured batching
// and retry policies.
type Engine struct {
	relayID                       string
	storage                       Storage
	publisher                     Publisher
	interval                      time.Duration
	leaseTimeout                  time.Duration
	reapBatchSize                 int
	batchSize                     int
	publisherConnectRetryInterval time.Duration
	enableBatchPublish            bool
	policy                        RetryPolicy
	logger                        *zap.Logger
	metrics                       *telemetry.Metrics
	tracer                        trace.Tracer
	meter                         metric.Meter
	events                        []Event
}

// EngineParams handles the tuning and identity.
// It encapsulates all the operational parameters required to initialize
// and configure the relay engine's behavior.
type EngineParams struct {
	RelayID                       string
	Interval                      time.Duration
	BatchSize                     int
	LeaseTimeout                  time.Duration
	ReapBatchSize                 int
	PublisherConnectRetryInterval time.Duration
	RetryPolicy                   RetryPolicy
	EnableBatchPublish            bool
}

// NewEngine initializes and returns a new Engine instance.
// It sets up the internal state, pre-allocates memory buffers for batching,
// and ensures that a unique RelayID is assigned if one is not provided in
// the parameters.
func NewEngine(
	storage Storage,
	publisher Publisher,
	params EngineParams,
	tel telemetry.Telemetry,
) (*Engine, error) {

	if params.BatchSize <= 0 {
		return nil, fmt.Errorf("engine cannot poll with batch size 0")
	}

	return &Engine{
		relayID:                       params.RelayID,
		storage:                       storage,
		publisher:                     publisher,
		interval:                      params.Interval,
		batchSize:                     params.BatchSize,
		leaseTimeout:                  params.LeaseTimeout,
		reapBatchSize:                 params.ReapBatchSize,
		publisherConnectRetryInterval: params.PublisherConnectRetryInterval,
		enableBatchPublish:            params.EnableBatchPublish,
		policy:                        params.RetryPolicy,
		logger:                        tel.ScopedLogger("engine"),
		metrics:                       tel.Metrics,
		tracer:                        tel.Tracer,
		meter:                         tel.Meter,
		events:                        make([]Event, params.BatchSize),
	}, nil
}

// Start initiates the relay's operational loops in the background.
// It launches three concurrent processes:
// 1. A metrics watcher that periodically updates backlog statistics.
// 2. A lease reaper that recovers "stuck" events from crashed instances.
// 3. The main event processing loop that moves messages from storage to the publisher.
// It blocks until the context is cancelled or a critical error occurs.
func (e *Engine) Start(ctx context.Context) error {

	// Ensure the publisher is ready before doing anything else.
	for {
		e.logger.Info("attempting to connect to publisher...",
			zap.String("relay_id", e.relayID))

		err := e.publisher.Connect(ctx)
		if err == nil {
			e.logger.Info("connected to publisher")
			break
		}

		e.logger.Warn("publisher is not ready, retrying...",
			zap.Error(err),
			zap.Duration("retry_interval", e.publisherConnectRetryInterval))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(e.publisherConnectRetryInterval):
			continue
		}
	}

	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return e.watchBacklog(gCtx)
	})

	g.Go(func() error {
		return e.reapExpiredLeases(gCtx)
	})

	g.Go(func() error {
		for {
			select {
			case <-gCtx.Done():
				return gCtx.Err()
			default:
				count, err := e.process(gCtx)
				if err != nil {
					e.logIfError(err, "Batch processing failed")
					// On error, wait before retrying to prevent log spam/CPU burn
					select {
					case <-gCtx.Done():
						return gCtx.Err()
					case <-time.After(e.interval):
						continue
					}
				}

				// If the batch was empty, wait for the next interval
				if count == 0 {
					select {
					case <-gCtx.Done():
						return gCtx.Err()
					case <-time.After(e.interval):
						continue
					}
				}
				// continue the loop immediately to "drain" the queue.
			}
		}
	})

	e.logger.Info("Engine started", zap.String("relay_id", e.relayID))

	return g.Wait()

}

// Stop performs a graceful shutdown of the Engine.
// It closes the underlying storage and publisher connections to ensure no data loss.
func (e *Engine) Stop(ctx context.Context) error {
	e.logger.Info("Stopping engine: shutting down storage and publisher...")

	var errs []error

	if err := e.storage.Close(ctx); err != nil {
		errs = append(errs, fmt.Errorf("storage close: %w", err))
	}

	if err := e.publisher.Close(ctx); err != nil {
		errs = append(errs, fmt.Errorf("publisher close: %w", err))
	}

	if len(errs) > 0 {
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

	e.logIfError(err, "failed to fetch events.", zap.Error(err))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_, err := e.storage.ReapExpiredLeases(ctx, e.leaseTimeout, e.reapBatchSize)
			e.logIfError(err, "failed to fetch events.", zap.Error(err))
		}
	}
}

func (e *Engine) process(ctx context.Context) (int, error) {

	e.logger.Debug("Engine processing...")

	ctx, span := e.tracer.Start(ctx, "Engine.Process",
		trace.WithAttributes(attribute.Int("batch.size_requested", e.batchSize)))
	defer span.End()

	events, err := e.claimBatch(ctx)
	if err != nil || len(events) == 0 {
		return 0, err
	}

	var successIDs []uuid.UUID
	var failedEvents []FailedEvent

	if e.enableBatchPublish {
		// successIDs, failedEvents, err = e.publishBatch(ctx, events)
		return 0, fmt.Errorf("batch publishing is not enabled yet")
	}

	successIDs, failedEvents, err = e.publishOnByOne(ctx, events)

	if err != nil {
		return 0, err
	}

	if len(successIDs) > 0 {
		if err := e.markDelivered(ctx, successIDs); err != nil {
			return 0, err
		}
	}

	if len(failedEvents) > 0 {
		if err := e.markFailed(ctx, failedEvents); err != nil {
			return 0, err
		}
	}

	e.logger.Info("process completed",
		zap.Int("count", len(events)),
		zap.Int("successful", len(successIDs)),
		zap.Int("failed", len(failedEvents)),
	)

	return len(events), nil
}

func (e *Engine) claimBatch(ctx context.Context) ([]Event, error) {
	ctx, span := e.tracer.Start(ctx, "Storage.ClaimBatch")
	defer span.End()

	start := time.Now()

	events, err := e.storage.ClaimBatch(ctx, e.relayID, e.batchSize, e.events)

	e.metrics.StorageLatency.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("op", "claim"),
			attribute.String("relay_id", e.relayID)),
	)
	e.metrics.BatchSize.Record(ctx, int64(len(events)),
		metric.WithAttributes(
			attribute.String("relay_id", e.relayID)),
	)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.logIfError(err, "failed to fetch events", zap.Error(err))
		return nil, err
	}

	span.SetAttributes(attribute.Int("batch.size_actual", len(events)))

	return events, nil
}

func (e *Engine) markDelivered(ctx context.Context, ids []uuid.UUID) error {
	ctx, span := e.tracer.Start(ctx, "Storage.MarkDeliveredBatch",
		trace.WithAttributes(attribute.Int("batch.size", len(ids))))
	defer span.End()

	start := time.Now()
	err := e.storage.MarkDeliveredBatch(ctx, ids, e.relayID)

	status := "success"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.logIfError(err, "failed to mark batch as delivered", zap.Error(err))
	}

	e.metrics.StorageLatency.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("op", "mark_delivered"),
			attribute.String("status", status),
			attribute.String("relay_id", e.relayID),
		),
	)

	return err
}

func (e *Engine) markFailed(ctx context.Context, failures []FailedEvent) error {
	ctx, span := e.tracer.Start(ctx, "Storage.MarkFailedBatch",
		trace.WithAttributes(attribute.Int("batch.size", len(failures))))
	defer span.End()

	start := time.Now()
	err := e.storage.MarkFailedBatch(ctx, failures, e.relayID)

	status := "success"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.logIfError(err, "failed to mark failure batch", zap.Error(err))
	}

	e.metrics.StorageLatency.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("op", "mark_failed"),
			attribute.String("status", status),
			attribute.String("relay_id", e.relayID),
		),
	)

	return err
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

			failedEvent, errorCode := e.assessFailure(event, err)
			failedEvents = append(failedEvents, failedEvent)
			e.metrics.EventsTotal.Add(
				ctx,
				1,
				metric.WithAttributes(
					attribute.String("status", "failed"),
					attribute.String("type", event.Type),
					attribute.String("code", errorCode),
					attribute.String("relay_id", e.relayID),
				),
			)

			continue
		}

		successEvents = append(successEvents, event.ID)

		e.metrics.EndToEndLatency.Record(ctx, time.Since(event.CreatedAt).Seconds(),
			metric.WithAttributes(
				attribute.String("type", event.Type),
				attribute.String("relay_id", e.relayID),
			),
		)
		e.metrics.EventsTotal.Add(
			ctx,
			1,
			metric.WithAttributes(
				attribute.String("status", "success"),
				attribute.String("type", event.Type),
				attribute.String("relay_id", e.relayID),
			),
		)

		e.logger.Debug("event published",
			zap.String("event_id", event.ID.String()),
			zap.String("type", event.Type),
		)
	}

	return successEvents, failedEvents, nil
}

// func (e *Engine) publishBatch(
// 	ctx context.Context,
// 	events []Event,
// ) ([]uuid.UUID, []FailedEvent, error) {

// 	// err := e.publisher.PublishBatch(ctx, events)
// 	err := fmt.Errorf("batch publishing is not enabled yet")
// 	if err != nil {
// 		if errors.Is(err, context.Canceled) {
// 			return nil, nil, err
// 		}

// 		failures := make([]FailedEvent, 0, len(events))
// 		for _, ev := range events {
// 			failures = append(failures, e.assessFailure(ev, err))
// 		}

// 		e.metrics.EventsTotal.Add(ctx, int64(len(events)),
// 			metric.WithAttributes(attribute.String("status", "failed")))

// 		return nil, failures, nil
// 	}

// 	successIDs := make([]uuid.UUID, 0, len(events))
// 	for _, ev := range events {
// 		successIDs = append(successIDs, ev.ID)
// 		e.metrics.EndToEndLatency.Record(ctx, time.Since(ev.CreatedAt).Seconds(),
// 			metric.WithAttributes(attribute.String("type", ev.Type)))
// 	}

// 	e.metrics.EventsTotal.Add(ctx, int64(len(events)),
// 		metric.WithAttributes(attribute.String("status", "success")))

// 	return successIDs, nil, nil
// }

func (e *Engine) assessFailure(event Event, publishError error) (FailedEvent, string) {
	nextAttempts := event.Attempts + 1
	delay, policyAllowsRetry := e.policy.NextBackoff(nextAttempts)

	isRetryable := true
	errorCode := "UNKNOWN"

	var pErr *PublishError
	if errors.As(publishError, &pErr) {
		isRetryable = pErr.IsRetryable
		if pErr.Code != "" {
			errorCode = pErr.Code
		}
	}

	shouldRetry := policyAllowsRetry && isRetryable

	result := FailedEvent{
		ID:        event.ID,
		Attempts:  nextAttempts,
		LastError: publishError.Error(),
	}

	if shouldRetry {
		result.NewStatus = EventStatusPending
		result.AvailableAt = time.Now().Add(delay)
	} else {
		result.NewStatus = EventStatusDead
		result.AvailableAt = time.Now()

		if !isRetryable {
			e.logger.Warn("event killed: non-retryable error",
				zap.String("event_id", event.ID.String()),
				zap.String("type", event.Type),
				zap.Error(publishError),
			)
		}
	}

	return result, errorCode
}

func (e *Engine) updateBacklogMetrics(ctx context.Context) {

	stats, err := e.storage.GetStats(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			e.logger.Warn("telemetry: failed to retrieve backlog stats",
				zap.Error(err),
				zap.String("relay_id", e.relayID),
			)
		}
		return
	}

	e.metrics.PendingGauge.Record(ctx, stats.PendingCount,
		metric.WithAttributes(
			attribute.String("status", "new"),
			attribute.String("relay_id", e.relayID),
		),
	)

	e.metrics.PendingGauge.Record(ctx, stats.RetryingCount,
		metric.WithAttributes(
			attribute.String("status", "retrying"),
			attribute.String("relay_id", e.relayID),
		),
	)

	e.metrics.OldestPendingSeconds.Record(ctx, stats.OldestAgeSec,
		metric.WithAttributes(
			attribute.String("relay_id", e.relayID),
		),
	)
}

func (e *Engine) logIfError(err error, msg string, fields ...zap.Field) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	e.logger.Error(msg, fields...)
}
