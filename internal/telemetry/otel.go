package telemetry

import (
	"context"
	"errors"

	"github.com/open-outbox/relay/internal/config"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0" // Ensure this is imported
)

// OTelProviders holds the concrete SDK providers (for shutdown)
// and the initialized interfaces (for injection).
type OTelProviders struct {
	TraceProvider *trace.TracerProvider
	MeterProvider *metric.MeterProvider
	Shutdown      func(context.Context) error
}

const serviceName = "open-outbox-relay"

// NewOTelProviders bootstraps the OpenTelemetry pipeline.
func NewOTelProviders(ctx context.Context, cfg *config.Config) (*OTelProviders, error) {
	var shutdownFuncs []func(context.Context) error
	var err error

	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	// Set up propagator.
	otel.SetTextMapPropagator(newPropagator())

	// Set up Shared Resource.
	res, err := newResource(cfg)
	if err != nil {
		return nil, errors.Join(err, shutdown(ctx))
	}

	// Set up trace provider.
	tp, err := newTracerProvider(res)
	if err != nil {
		return nil, errors.Join(err, shutdown(ctx))
	}
	shutdownFuncs = append(shutdownFuncs, tp.Shutdown)
	otel.SetTracerProvider(tp)

	// Set up meter provider.
	mp, err := newMeterProvider(res)
	if err != nil {
		return nil, errors.Join(err, shutdown(ctx))
	}
	shutdownFuncs = append(shutdownFuncs, mp.Shutdown)
	otel.SetMeterProvider(mp)

	return &OTelProviders{
		TraceProvider: tp,
		MeterProvider: mp,
		Shutdown:      shutdown,
	}, nil
}

func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func newResource(cfg *config.Config) (*resource.Resource, error) {

	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			"",
			semconv.ServiceName(serviceName),
			attribute.String("relay.id", cfg.RelayID),
		))
}

func newTracerProvider(res *resource.Resource) (*trace.TracerProvider, error) {
	ctx := context.Background()

	traceExporter, err := autoexport.NewSpanExporter(ctx)

	if err != nil {
		return nil, err
	}

	tp := trace.NewTracerProvider(
		trace.WithBatcher(traceExporter),
		trace.WithResource(res),
	)
	return tp, nil
}

func newMeterProvider(res *resource.Resource) (*metric.MeterProvider, error) {
	ctx := context.Background()
	batchBuckets := []float64{1, 5, 10, 25, 50, 75, 100, 250, 500, 1000, 2500, 5000, 10000}
	latencyBuckets := []float64{
		.002, // 2ms   - Ideal for local DB/caching hits
		.005, // 5ms   - Fast processing
		.01,  // 10ms  - Normal operational floor
		.025, // 25ms  - Standard network roundtrip
		.05,  // 50ms
		.1,   // 100ms - The "Snappy" threshold
		.25,  // 250ms
		.5,   // 500ms
		1,    // 1s    - The SLO boundary (Warning)
		2.5,  // 2.5s  - Heavy congestion
		5,    // 5s    - Timeout territory
		10,   // 10s   - Critical failure/Deadlock
		30,   // 30s   - Dead
	}

	e2eLatencyView := metric.NewView(
		metric.Instrument{Name: "openoutbox.events.e2e_latency"},
		metric.Stream{
			Aggregation: metric.AggregationExplicitBucketHistogram{
				Boundaries: latencyBuckets,
			},
		},
	)

	latencyView := metric.NewView(
		metric.Instrument{Name: "openoutbox.*.latency"},
		metric.Stream{
			Aggregation: metric.AggregationExplicitBucketHistogram{
				Boundaries: latencyBuckets,
			},
		},
	)

	batchView := metric.NewView(
		metric.Instrument{Name: "openoutbox.events.batch_size"},
		metric.Stream{
			Aggregation: metric.AggregationExplicitBucketHistogram{
				Boundaries: batchBuckets,
			},
		},
	)
	metricReader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return nil, err
	}

	mp := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metricReader),
		metric.WithView(e2eLatencyView),
		metric.WithView(latencyView),
		metric.WithView(batchView),
	)

	return mp, nil
}
