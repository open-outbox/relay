package telemetry

import (
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds the set of OpenTelemetry metric instruments used to monitor
// the health and performance of the Outbox Relay.
type Metrics struct {
	// BatchSize tracks the number of events fetched from the database in a
	// single claim operation. This helps monitor if the relay is keeping
	// up with the ingestion rate.
	BatchSize metric.Int64Histogram

	// EventsTotal is a counter that tracks the total number of events processed.
	// Typical attributes include 'status' (success/failed) and 'type' (event type).
	EventsTotal metric.Int64Counter

	// EndToEndLatency measures the total time elapsed from the moment an event
	// was created in the database until it was successfully acknowledged by
	// the message broker.
	// Typical attributes include 'type' (event type).
	EndToEndLatency metric.Float64Histogram

	// StorageLatency measures the duration of individual database operations.
	// Typical attributes include 'op' (e.g., claim, mark_delivered, mark_failed).
	StorageLatency metric.Float64Histogram

	// PublisherLatency measures how long it takes to publish a message to the
	// broker.
	// Typical attributes include 'status' (success/failed) and 'type' (event type).
	PublisherLatency metric.Float64Histogram

	// PendingGauge represents the current number of events sitting in the
	// outbox table with a 'PENDING' status.
	PendingGauge metric.Int64Gauge

	// OldestPendingSeconds tracks the age of the oldest pending event in the
	// queue, providing a direct measurement of "lag".
	OldestPendingSeconds metric.Int64Gauge

	// RelayStatusGauge represents the current operational state of the relay:
	// 1 = Active, 2 = Paused (Publisher Down), 3 = Error (Infrastructure Failure).
	RelayStateGauge metric.Int64Gauge
}

// NewMetrics initializes the Metrics struct by creating instruments through
// the provided MeterProvider. It defines the descriptions and units for
// each metric to ensure they are correctly represented in observability
// backends.
func NewMetrics(meterProvider metric.MeterProvider) (*Metrics, error) {
	meter := meterProvider.Meter(InstrumentationName)
	m := &Metrics{}
	var err error

	m.BatchSize, err = meter.Int64Histogram(
		"openoutbox.events.batch_size",
		metric.WithDescription("Number of events claimed from the database in a single batch."),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}

	m.EventsTotal, err = meter.Int64Counter(
		"openoutbox.events.total",
		metric.WithDescription("Total number of events processed by the relay."),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}

	m.EndToEndLatency, err = meter.Float64Histogram(
		"openoutbox.events.e2e_latency",
		metric.WithDescription(
			"Time from event creation in DB to successful publication (seconds).",
		),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	m.StorageLatency, err = meter.Float64Histogram(
		"openoutbox.storage.latency",
		metric.WithDescription("Latency of database operations (seconds)."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	m.PublisherLatency, err = meter.Float64Histogram(
		"openoutbox.publisher.latency",
		metric.WithDescription("Latency of message broker publication (seconds)."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	m.PendingGauge, err = meter.Int64Gauge(
		"openoutbox.backlog.pending_count",
		metric.WithDescription("Current count of pending events in the outbox table."),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}

	m.OldestPendingSeconds, err = meter.Int64Gauge(
		"openoutbox.backlog.oldest_age_seconds",
		metric.WithDescription("Age of the oldest pending event in the outbox table."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	m.RelayStateGauge, err = meter.Int64Gauge(
		"openoutbox.relay.state",
		metric.WithDescription("Current state of of the relay engine."),
		metric.WithUnit(""),
	)
	if err != nil {
		return nil, err
	}

	return m, nil
}
