package telemetry

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmeter "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0" // Ensure this is imported
	"go.opentelemetry.io/otel/trace"
)

// OTelProviders holds the concrete SDK providers (for shutdown)
// and the initialized interfaces (for injection).
type OTelProviders struct {
	Tracer   trace.Tracer
	Meter    metric.Meter
	Shutdown func(context.Context) error
}

const serviceName = "openoutbox-relay"

// NewOTelProviders bootstraps the OpenTelemetry pipeline.
func NewOTelProviders(ctx context.Context) (*OTelProviders, error) {
	var shutdownFuncs []func(context.Context) error

	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	otel.SetTextMapPropagator(newPropagator())

	// Initialize Shared Resource
	res, err := newResource()
	if err != nil {
		return nil, errors.Join(err, shutdown(ctx))
	}

	// 1. Initialize Tracer Provider
	tp, err := newTracerProvider(res)
	if err != nil {
		return nil, errors.Join(err, shutdown(ctx))
	}
	shutdownFuncs = append(shutdownFuncs, tp.Shutdown)
	otel.SetTracerProvider(tp)

	// 2. Initialize Meter Provider
	mp, err := newMeterProvider(res)
	if err != nil {
		return nil, errors.Join(err, shutdown(ctx))
	}
	shutdownFuncs = append(shutdownFuncs, mp.Shutdown)
	otel.SetMeterProvider(mp)

	return &OTelProviders{
		Tracer:   tp.Tracer(serviceName),
		Meter:    mp.Meter(serviceName),
		Shutdown: shutdown,
	}, nil
}

func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func newResource() (*resource.Resource, error) {
	// Using an empty string for the SchemaURL in NewWithAttributes
	// is the key to avoiding the "Conflicting Schema URL" error.
	extraRes := resource.NewWithAttributes(
		"",
		semconv.ServiceNameKey.String(serviceName),
		semconv.ServiceVersionKey.String("1.0.0"),
	)

	return resource.Merge(
		resource.Default(),
		extraRes,
	)
}

func newTracerProvider(res *resource.Resource) (*sdktrace.TracerProvider, error) {
	traceExporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter, sdktrace.WithBatchTimeout(time.Second)),
		sdktrace.WithResource(res),
	)
	return tp, nil
}

func newMeterProvider(res *resource.Resource) (*sdkmeter.MeterProvider, error) {
	metricExporter, err := stdoutmetric.New(stdoutmetric.WithPrettyPrint())
	if err != nil {
		return nil, err
	}

	mp := sdkmeter.NewMeterProvider(
		sdkmeter.WithResource(res),
		sdkmeter.WithReader(sdkmeter.NewPeriodicReader(metricExporter,
			sdkmeter.WithInterval(3*time.Second))),
	)
	return mp, nil
}
