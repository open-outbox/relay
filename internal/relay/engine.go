package relay

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/telemetry"
	"github.com/open-outbox/relay/internal/utils"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

const (
	defaultPollInterval                  = 500 * time.Millisecond
	defaultLeaseTimeout                  = 3 * time.Minute
	defaultWatchInterval                 = 30 * time.Second
	defaultHealthCheckInterval           = 5 * time.Second
	defaultPublisherConnectRetryInterval = 5 * time.Second
)

// EngineMode dictates the runtime profile of this Relay instance.
//
// By default, a single instance executes all core, maintenance, and metrics routines
// concurrently. For high-scale or performance-critical production topologies, instances
// can be specialized into dedicated roles to protect the primary database from
// analytical query thrashing or lock contention.
type EngineMode string

const (
	// EngineModeDefault runs the full suite of background routines concurrently.
	// This includes the core polling engine, the lease reaper,
	// and the heavy database telemetry/analytical statistics loops.
	// Recommended for local development and standard workloads.
	EngineModeDefault EngineMode = "default"

	// EngineModeWorker focuses strictly on high-throughput event processing.
	// It only runs the core engine loops (polling and publishing)
	EngineModeWorker EngineMode = "worker"

	// EngineModeMaintenance isolates heavy, non-critical database operations.
	// It completely disables core event publishing, operating purely as a single-replica
	// service dedicated to executing periodic lease reaping and heavy background analytical
	// queries for system observability dashboards.
	EngineModeMaintenance EngineMode = "maintenance"
)

// IsValid checks if the mode is a supported engine state
func (m EngineMode) IsValid() bool {
	switch m {
	case EngineModeDefault, EngineModeWorker, EngineModeMaintenance:
		return true
	default:
		return false
	}
}

// State represents the current operational state of the engine.
// It is reported via the StateGauge to provide observability into
// whether the engine is healthy, throttled, or failing.
type State int64

// Relay Status Constants
const (
	// StateActive: Everything is fine. The engine is polling.
	StateActive State = 1
	// StatePaused: The publisher (Kafka/NATS) is down. We are standing by.
	StatePaused State = 2
	// StateError: A critical error occurred (like a DB connection failure).
	StateError State = 3
)

// ErrPublisherPaused is returned when the engine cannot proceed because
// the publisher (e.g., Kafka, NATS, Redis) is currently unreachable or down.
var ErrPublisherPaused = errors.New("publisher is paused")

// Engine coordinates the movement of events from Storage to Publisher.
// It manages the polling loop, background maintenance tasks like lease reaping,
// and ensures that events are processed according to the configured batching
// and retry policies.
type Engine struct {
	relayID                       string
	engineMode                    EngineMode
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
	isHealthy                     atomic.Bool
	healthCheckInterval           time.Duration
	lastStatus                    State
	enableStats                   bool
	enableReaper                  bool
}

// EngineParams handles the tuning and identity.
// It encapsulates all the operational parameters required to initialize
// and configure the relay engine's behavior.
type EngineParams struct {
	RelayID                       string
	EngineMode                    EngineMode
	Interval                      time.Duration
	BatchSize                     int
	LeaseTimeout                  time.Duration
	ReapBatchSize                 int
	PublisherConnectRetryInterval time.Duration
	HealthCheckInterval           time.Duration
	RetryPolicy                   RetryPolicy
	EnableBatchPublish            bool
	EnableStats                   bool
	EnableReaper                  bool
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

	if params.Interval <= 0 {
		params.Interval = defaultPollInterval
	}

	if params.LeaseTimeout <= 0 {
		params.LeaseTimeout = defaultLeaseTimeout
	}

	if params.HealthCheckInterval <= 0 {
		params.HealthCheckInterval = defaultHealthCheckInterval
	}

	if params.PublisherConnectRetryInterval <= 0 {
		params.PublisherConnectRetryInterval = defaultPublisherConnectRetryInterval
	}

	if params.ReapBatchSize <= 0 {
		params.ReapBatchSize = params.BatchSize
	}

	mode := EngineMode(params.EngineMode)
	if params.EngineMode == "" {
		mode = EngineModeDefault
	} else if !mode.IsValid() {
		return nil, fmt.Errorf("invalid ENGINE_MODE %q: supported modes are %s, %s, or %s",
			params.EngineMode,
			EngineModeDefault,
			EngineModeWorker,
			EngineModeMaintenance,
		)
	}

	params.EngineMode = mode

	return &Engine{
		relayID:                       params.RelayID,
		engineMode:                    params.EngineMode,
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
		healthCheckInterval:           params.HealthCheckInterval,
		enableStats:                   params.EnableStats,
		enableReaper:                  params.EnableReaper,
	}, nil
}

// Start initiates the relay's operational loops in the background.
// It launches three concurrent processes:
// 1. A metrics watcher that periodically updates backlog statistics.
// 2. A lease reaper that recovers "stuck" events from crashed instances.
// 3. The main event processing loop that moves messages from storage to the publisher.
// It blocks until the context is cancelled or a critical error occurs.
func (e *Engine) Start(ctx context.Context) error {

	g, gCtx := errgroup.WithContext(ctx)

	var runEngine, runStats, runReaper bool

	switch e.engineMode {
	case EngineModeDefault:
		e.logger.Info("starting engine in DEFAULT mode")
		runEngine = true
		runStats = e.enableStats
		runReaper = e.enableReaper

	case EngineModeMaintenance:
		e.logger.Info("starting engine in MAINTENANCE mode")
		runEngine = false
		runStats = e.enableStats
		runReaper = e.enableReaper

	case EngineModeWorker:
		e.logger.Info("starting engine in WORKER mode")
		runEngine = true
		runStats = false
		runReaper = false

	default:
		return fmt.Errorf("unknown or unsupported engine mode: %q", e.engineMode)
	}

	if runEngine {
		if err := e.connectToPublisher(ctx); err != nil {
			return err
		}
		e.lastStatus = StateActive

		g.Go(func() error {
			return e.monitorHealth(gCtx)
		})
		g.Go(func() error { return e.runProcessingLoop(gCtx) })
	}
	if runStats {
		e.logger.Info("stats monitor enabled")
		g.Go(func() error { return e.watchBacklog(gCtx) })
	}
	if runReaper {
		e.logger.Info("lease reaper enabled")
		g.Go(func() error { return e.reapExpiredLeases(gCtx) })
	}

	e.logger.Info("engine started successfully", zap.String("relay_id", e.relayID))
	return g.Wait()
}

func (e *Engine) runProcessingLoop(ctx context.Context) error {
	e.logger.Info("starting processing loop", zap.String("relay_id", e.relayID))

	// Initiall jitter to prevent thundering herd in startup
	initJitter := time.Duration(rand.IntN(50)) * time.Millisecond
	if initJitter > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(initJitter):
		}
	}

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	var waitInterval time.Duration

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		count, err := e.process(ctx)

		var currentStatus State
		if err != nil {
			if errors.Is(err, ErrPublisherPaused) {
				currentStatus = StatePaused
				waitInterval = e.healthCheckInterval
			} else {
				currentStatus = StateError
				waitInterval = e.interval
				utils.LogIfError(e.logger, err, "Batch processing failed")
			}
		} else {
			currentStatus = StateActive
			if count > 0 {
				waitInterval = 0
			} else {
				// jitter to prevent thundering herd on sudden event bursts
				jitter := time.Duration(rand.IntN(50)) * time.Millisecond
				waitInterval = e.interval + jitter
			}
		}

		if currentStatus != e.lastStatus {
			e.metrics.RelayStateGauge.Record(ctx, int64(currentStatus),
				metric.WithAttributes(attribute.String("relay_id", e.relayID)),
			)
			e.lastStatus = currentStatus
		}

		if waitInterval == 0 {
			continue
		}

		timer.Reset(waitInterval)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
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
	// decouple interval from leaseTimeout. Reaping doesn't need to happen
	// every millisecond a lease expires.
	// a sensible default is half the lease timeout, or a
	// fixed background cadence (e.g., 30s to 1m).
	baseInterval := e.leaseTimeout / 2
	if baseInterval > 1*time.Minute {
		baseInterval = 1 * time.Minute
	}

	// calculates next tick with 20% random jitter
	getJitteredDuration := func(base time.Duration) time.Duration {
		jitter := time.Duration(rand.Int64N(int64(base / 5)))
		return base + jitter
	}

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	for {
		rowsAffected, err := e.storage.ReapExpiredLeases(ctx, e.leaseTimeout, e.reapBatchSize)
		utils.LogIfError(e.logger, err, "failed to reap expired leases.", zap.Error(err))

		var nextSleep time.Duration
		if err == nil && rowsAffected >= int64(e.reapBatchSize) {
			// If we maxed out the batch, there's more garbage to collect.
			// Sleep briefly (e.g., 100ms) and loop again immediately to drain it
			nextSleep = 100 * time.Millisecond
		} else {
			// Database is clean, sleep for a long, jittered interval
			nextSleep = getJitteredDuration(baseInterval)
		}

		timer.Reset(nextSleep)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (e *Engine) process(ctx context.Context) (int, error) {

	if !e.isHealthy.Load() {
		return 0, ErrPublisherPaused
	}

	e.logger.Debug("Engine processing...")

	ctx, span := e.tracer.Start(ctx, "Engine.Process",
		trace.WithAttributes(attribute.Int("batch.size_requested", e.batchSize)))
	defer span.End()

	events, err := e.storage.ClaimBatch(ctx, e.batchSize, e.events)
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
		if _, err := e.storage.MarkDeliveredBatch(ctx, successIDs); err != nil {
			return 0, err
		}
	}

	if len(failedEvents) > 0 {
		if _, err := e.storage.MarkFailedBatch(ctx, failedEvents); err != nil {
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
			e.metrics.EventsTotal.Add(
				ctx,
				1,
				metric.WithAttributes(
					attribute.String("status", "failed"),
				),
			)

			continue
		}

		successEvents = append(successEvents, event.ID)

		e.metrics.EndToEndLatency.Record(ctx, time.Since(event.CreatedAt).Seconds(),
			metric.WithAttributes(
				attribute.String("relay_id", e.relayID),
			),
		)
		e.metrics.EventsTotal.Add(
			ctx,
			1,
			metric.WithAttributes(
				attribute.String("status", "success"),
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
// 		e.metrics.EndToEndLatency.Record(ctx, time.Since(ev.CreatedAt).Seconds())
// 	}

// 	e.metrics.EventsTotal.Add(ctx, int64(len(events)),
// 		metric.WithAttributes(attribute.String("status", "success")))

// 	return successIDs, nil, nil
// }

func (e *Engine) assessFailure(event Event, publishError error) FailedEvent {
	nextAttempts := event.Attempts + 1
	delay, policyAllowsRetry := e.policy.NextBackoff(nextAttempts)

	isRetryable := true
	var pErr *PublishError
	if errors.As(publishError, &pErr) {
		isRetryable = pErr.IsRetryable
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

	return result
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

func (e *Engine) connectToPublisher(ctx context.Context) error {
	// Ensure the publisher is ready before doing anything else.
	for {
		e.logger.Info("attempting to connect to publisher...",
			zap.String("relay_id", e.relayID))

		err := e.publisher.Connect(ctx)
		if err == nil {
			e.metrics.RelayStateGauge.Record(ctx, int64(StateActive),
				metric.WithAttributes(
					attribute.String("relay_id", e.relayID),
				),
			)
			e.isHealthy.Store(true)
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
	return nil
}

func (e *Engine) monitorHealth(ctx context.Context) error {
	ticker := time.NewTicker(e.healthCheckInterval)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := e.publisher.Ping(ctx); err != nil {
				if e.isHealthy.Swap(false) {
					e.metrics.RelayStateGauge.Record(ctx, int64(StatePaused),
						metric.WithAttributes(
							attribute.String("relay_id", e.relayID),
						),
					)
					e.logger.Error(
						"Publisher health check failed. Pausing engine.",
						zap.String("relay_id", e.relayID),
					)
				}
			} else {
				if !e.isHealthy.Swap(true) {
					e.metrics.RelayStateGauge.Record(ctx, int64(StateActive),
						metric.WithAttributes(
							attribute.String("relay_id", e.relayID),
						),
					)
					e.logger.Info("Publisher health restored. Resuming engine.", zap.String("relay_id", e.relayID))
				}
			}
		}
	}
}
