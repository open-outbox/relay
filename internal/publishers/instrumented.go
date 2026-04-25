package publishers

import (
	"context"
	"errors"
	"time"

	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// InstrumentedPublisher is a decorator that wraps a base relay.Publisher
// to provide transparent observability. It automatically records OpenTelemetry
// traces, Prometheus metrics, and structured logs for every publish operation.
type InstrumentedPublisher struct {
	publisher relay.Publisher
	logger    *zap.Logger
	metrics   *telemetry.Metrics
	tracer    trace.Tracer
	meter     metric.Meter
	relayID   string
}

// NewInstrumentedPublisher returns a new publisher wrapper configured with the
// provided telemetry components.
func NewInstrumentedPublisher(
	p relay.Publisher,
	tel telemetry.Telemetry,
	relayID string,
) *InstrumentedPublisher {
	return &InstrumentedPublisher{
		publisher: p,
		logger:    tel.ScopedLogger("publisher"),
		metrics:   tel.Metrics,
		tracer:    tel.Tracer,
		meter:     tel.Meter,
		relayID:   relayID,
	}
}

// Connect establishes the connection to the underlying message broker.
// can initialize the connection through the instrumentation layer.
func (ip *InstrumentedPublisher) Connect(ctx context.Context) error {
	return ip.publisher.Connect(ctx)
}

// Publish wraps the underlying publisher's Publish method. It creates a new
// trace span, records the start time for latency metrics, and captures any
// errors. It ensures that delivery metrics include both the event type and
// the final outcome (success/failure) for granular monitoring.
func (ip *InstrumentedPublisher) Publish(ctx context.Context, event relay.Event) error {
	ctx, span := ip.tracer.Start(ctx, "Publisher.Publish",
		trace.WithAttributes(
			attribute.String("event.id", event.ID.String()),
			attribute.String("event.type", event.Type),
			attribute.Int("event.attempt", event.Attempts),
		))
	defer span.End()

	start := time.Now()
	err := ip.publisher.Publish(ctx, event)

	// Move metric recording here so we can include the outcome
	status := "success"
	if err != nil {
		status = "failed"

		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		if !errors.Is(err, context.Canceled) {
			ip.logger.Warn("publish failed",
				zap.String("event_id", event.ID.String()),
				zap.String("type", event.Type),
				zap.Error(err),
			)
		}
	} else {
		span.SetStatus(codes.Ok, "success")
	}

	// Record latency with BOTH type and status
	ip.metrics.PublisherLatency.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("type", event.Type),
			attribute.String("status", status),
			attribute.String("relay_id", ip.relayID),
		))

	return err
}

// Close closes the underlying publisher.
func (ip *InstrumentedPublisher) Close(ctx context.Context) error {
	return ip.publisher.Close(ctx)
}

// Ping verifies the connectivity of the underlying publisher to the message broker.
func (ip *InstrumentedPublisher) Ping(ctx context.Context) error {
	return ip.publisher.Ping(ctx)
}
