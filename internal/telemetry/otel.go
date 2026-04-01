package telemetry

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"

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

const serviceName = "openoutbox-relay"

// NewOTelProviders bootstraps the OpenTelemetry pipeline.
func NewOTelProviders(ctx context.Context) (*OTelProviders, error) {
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
	res, err := newResource()
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

func newResource() (*resource.Resource, error) {

	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			"",
			semconv.ServiceName("github.com/open-outbox/relay"),
		))
}

func newTracerProvider(res *resource.Resource) (*trace.TracerProvider, error) {
	traceExporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, err
	}

	tp := trace.NewTracerProvider(
		trace.WithBatcher(traceExporter, trace.WithBatchTimeout(time.Second)),
		trace.WithResource(res),
	)
	return tp, nil
}

func newMeterProvider(res *resource.Resource) (*metric.MeterProvider, error) {
	metricExporter, err := stdoutmetric.New(stdoutmetric.WithPrettyPrint())
	if err != nil {
		return nil, err
	}

	mp := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(metricExporter,
			metric.WithInterval(3*time.Second))),
	)
	return mp, nil
}
