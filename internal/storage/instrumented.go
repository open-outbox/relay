package storage

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/telemetry"
	"github.com/open-outbox/relay/internal/utils"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// Instrumented decorates a storage implementation with observability features.
// It records metrics, traces, and logs for all underlying storage operations.
type Instrumented struct {
	storage relay.Storage
	logger  *zap.Logger
	metrics *telemetry.Metrics
	tracer  trace.Tracer
	meter   metric.Meter
	relayID string
}

// NewInstrumented initializes the storage decorator with the required telemetry providers.
// It scopes the logger to "storage" and preserves the relay identity for metric attribution.
func NewInstrumented(
	s relay.Storage,
	tel telemetry.Telemetry,
	relayID string,
) *Instrumented {
	return &Instrumented{
		storage: s,
		logger:  tel.ScopedLogger("storage"),
		metrics: tel.Metrics,
		tracer:  tel.Tracer,
		meter:   tel.Meter,
		relayID: relayID,
	}
}

// ClaimBatch wraps the storage call to identify and lock events for processing.
// It records request latency and the actual number of events retrieved for observability.
func (i *Instrumented) ClaimBatch(
	ctx context.Context,
	batchSize int,
	buffer []relay.Event,
) ([]relay.Event, error) {
	ctx, span := i.tracer.Start(ctx, "Storage.ClaimBatch")
	defer span.End()

	start := time.Now()
	events, err := i.storage.ClaimBatch(ctx, batchSize, buffer)

	status := "success"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		utils.LogIfError(i.logger, err, "failed to fetch events", zap.Error(err))
	}

	i.metrics.StorageLatency.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("op", "claim"),
			attribute.String("status", status),
			attribute.String("relay_id", i.relayID)),
	)

	if err != nil {
		return nil, err
	}

	i.metrics.BatchSize.Record(ctx, int64(len(events)),
		metric.WithAttributes(
			attribute.String("relay_id", i.relayID)),
	)
	span.SetAttributes(attribute.Int("batch.size_actual", len(events)))

	return events, nil
}

// MarkDeliveredBatch transitions a batch of events to the final delivered state.
// It records completion latency and result status to track storage reliability.
func (i *Instrumented) MarkDeliveredBatch(
	ctx context.Context,
	ids []uuid.UUID,
) error {
	ctx, span := i.tracer.Start(ctx, "Storage.MarkDeliveredBatch",
		trace.WithAttributes(attribute.Int("batch.size", len(ids))))
	defer span.End()

	start := time.Now()
	err := i.storage.MarkDeliveredBatch(ctx, ids)

	status := "success"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		utils.LogIfError(i.logger, err, "failed to mark batch as delivered", zap.Error(err))
	}

	i.metrics.StorageLatency.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("op", "mark_delivered"),
			attribute.String("status", status),
			attribute.String("relay_id", i.relayID),
		),
	)

	return err
}

// MarkFailedBatch updates events with failure details and schedules retries.
// It tracks the storage latency and records whether the update was successful.
func (i *Instrumented) MarkFailedBatch(
	ctx context.Context,
	failures []relay.FailedEvent,
) error {

	ctx, span := i.tracer.Start(ctx, "Storage.MarkFailedBatch",
		trace.WithAttributes(attribute.Int("batch.size", len(failures))))
	defer span.End()

	start := time.Now()
	err := i.storage.MarkFailedBatch(ctx, failures)

	status := "success"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		utils.LogIfError(i.logger, err, "failed to mark failure batch", zap.Error(err))
	}

	i.metrics.StorageLatency.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("op", "mark_failed"),
			attribute.String("status", status),
			attribute.String("relay_id", i.relayID),
		),
	)

	return err
}

// ReapExpiredLeases recovers events stuck in 'DELIVERING' status due to worker failure.
// It instruments the cleanup process to monitor the health of the self-healing mechanism,
// and records the count of recovered events to monitor relay health and worker stability.
func (i *Instrumented) ReapExpiredLeases(
	ctx context.Context,
	leaseTimeout time.Duration,
	limit int,
) (int64, error) {

	ctx, span := i.tracer.Start(ctx, "Storage.ReapExpiredLeases",
		trace.WithAttributes(attribute.Int("batch.limit", limit)))
	defer span.End()

	start := time.Now()
	count, err := i.storage.ReapExpiredLeases(ctx, leaseTimeout, limit)

	status := "success"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		utils.LogIfError(i.logger, err, "failed to reap expired leases")
	} else if count > 0 {
		// Optional: Log when reaping actually happens so you have a trail in your logs
		i.logger.Info("successfully reaped expired leases",
			zap.Int64("count", count),
			zap.Duration("timeout", leaseTimeout))

		span.SetAttributes(attribute.Int64("reaped.count", count))
	}

	i.metrics.StorageLatency.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("op", "reap_expired_leases"),
			attribute.String("status", status),
			attribute.String("relay_id", i.relayID),
		),
	)

	if err == nil && count > 0 {
		i.metrics.ReapedTotal.Add(ctx, count,
			metric.WithAttributes(attribute.String("relay_id", i.relayID)),
		)
	}

	return count, err
}

// GetStats retrieves current backlog metrics and event counts from storage.
// It instruments the request to ensure health-monitoring queries remain performant.
func (i *Instrumented) GetStats(ctx context.Context) (relay.Stats, error) {
	ctx, span := i.tracer.Start(ctx, "Storage.GetStats")
	defer span.End()

	start := time.Now()
	stats, err := i.storage.GetStats(ctx)

	status := "success"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		utils.LogIfError(i.logger, err, "failed to get stats", zap.Error(err))
	}

	i.metrics.StorageLatency.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("op", "get_stats"),
			attribute.String("status", status),
			attribute.String("relay_id", i.relayID),
		),
	)

	return stats, err
}

// Prune removes processed and failed events from storage based on the provided retention policy.
// It tracks the operation's duration and success rate to monitor database maintenance health.
func (i *Instrumented) Prune(
	ctx context.Context,
	opts relay.PruneOptions,
) (relay.PruneResult, error) {
	ctx, span := i.tracer.Start(ctx, "Storage.Prune")
	defer span.End()

	start := time.Now()
	result, err := i.storage.Prune(ctx, opts)

	status := "success"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		utils.LogIfError(i.logger, err, "failed to prune events", zap.Error(err))
	}

	i.metrics.StorageLatency.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("op", "prune"),
			attribute.String("status", status),
			attribute.String("relay_id", i.relayID),
		),
	)

	return result, err
}

// Close gracefully shuts down the underlying storage.
func (i *Instrumented) Close(ctx context.Context) error {
	return i.storage.Close(ctx)
}

// Ping verifies the connectivity and health of the underlying storage system.
func (i *Instrumented) Ping(ctx context.Context) error {
	return i.storage.Ping(ctx)
}
