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
	Latency              metric.Float64Histogram
	PublisherLatency     metric.Float64Histogram
}

func NewMetrics(meterProvider metric.MeterProvider) (*Metrics, error) {
	var err error
	m := &Metrics{}
	meter := meterProvider.Meter(instrumentationName)

	m.Claimed, err = meter.Int64Counter(
		"openoutbox.claimed.total",
		metric.WithDescription("Total claimed events."),
	)
	if err != nil {
		return nil, err
	}

	m.Delivered, err = meter.Int64Counter(
		"openoutbox.delivered.total",
		metric.WithDescription("Total delivered events."),
	)
	if err != nil {
		return nil, err
	}

	m.Failed, err = meter.Int64Counter(
		"openoutbox.failed.total",
		metric.WithDescription("Total failed deliveries."),
	)
	if err != nil {
		return nil, err
	}

	m.PendingGauge, err = meter.Int64Gauge(
		"openoutbox.pending.gauge",
		metric.WithDescription("Current count of pending events."),
	)
	if err != nil {
		return nil, err
	}

	m.OldestPendingSeconds, err = meter.Int64Gauge(
		"openoutbox.oldest_pending_seconds.gauge",
		metric.WithDescription("Age (seconds) of oldest pending event."),
	)
	if err != nil {
		return nil, err
	}

	m.Latency, err = meter.Float64Histogram(
		"openoutbox.latency.histogram",
		metric.WithDescription("Time taken to process a single batch."),
	)
	if err != nil {
		return nil, err
	}

	m.PublisherLatency, err = meter.Float64Histogram(
		"openoutbox.publisher_latency.histogram",
		metric.WithDescription("Time taken to publish events."),
	)
	if err != nil {
		return nil, err
	}

	return m, nil
}
