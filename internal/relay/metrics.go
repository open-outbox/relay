package relay

import (
	"go.opentelemetry.io/otel/metric"
)

type Metrics struct {
	Claimed              metric.Int64Counter
	Delivered            metric.Int64Counter
	Failed               metric.Int64Counter
	PendingGauge         metric.Int64Gauge
	OldestPendingSeconds metric.Int64Gauge
	// Errors metric.Int64Counter
	Latency          metric.Float64Histogram
	PublisherLatency metric.Float64Histogram
}

func NewMetrics(meter metric.Meter) (*Metrics, error) { // Add error return
	var err error
	m := &Metrics{}

	// We must handle the error for every instrument
	m.Claimed, err = meter.Int64Counter(
		"openoutbox_claimed_total",
		metric.WithDescription("Total claimed events."),
	)
	if err != nil {
		return nil, err
	}

	m.Delivered, err = meter.Int64Counter(
		"openoutbox_delivered_total",
		metric.WithDescription("Total delivered events."),
	)
	if err != nil {
		return nil, err
	}

	m.Failed, err = meter.Int64Counter(
		"openoutbox_failed_total",
		metric.WithDescription("Total failed deliveries."),
	)
	if err != nil {
		return nil, err
	}

	// Note: In OTEL, Gauge is "Int64Gauge" (Sync) or "Int64ObservableGauge" (Async)
	m.PendingGauge, err = meter.Int64Gauge(
		"openoutbox_pending_gauge",
		metric.WithDescription("Current count of pending events."),
	)
	if err != nil {
		return nil, err
	}

	m.OldestPendingSeconds, err = meter.Int64Gauge(
		"openoutbox_oldest_pending_seconds_gauge",
		metric.WithDescription("Age (seconds) of oldest pending event."),
	)
	if err != nil {
		return nil, err
	}

	m.Latency, err = meter.Float64Histogram(
		"openoutbox_latency_histogram",
		metric.WithDescription("Time taken to process a single batch."),
	)
	if err != nil {
		return nil, err
	}

	m.PublisherLatency, err = meter.Float64Histogram(
		"openoutbox_publisher_latency_histogram",
		metric.WithDescription("Time taken to publish events."),
	)
	if err != nil {
		return nil, err
	}

	return m, nil // Don't forget to return!
}
