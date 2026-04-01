package telemetry

import (
	"context"
	"errors"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmeter "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
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

	// Clean shutdown helper
	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	// Set up global propagator (TraceContext + Baggage)
	otel.SetTextMapPropagator(newPropagator())

	// 1. Initialize Tracer Provider
	tp, err := newTracerProvider()
	if err != nil {
		return nil, errors.Join(err, shutdown(ctx))
	}
	shutdownFuncs = append(shutdownFuncs, tp.Shutdown)
	otel.SetTracerProvider(tp)

	// 2. Initialize Meter Provider
	mp, err := newMeterProvider()
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
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			"",
			semconv.ServiceNameKey.String("openoutbox-relay"),
			semconv.ServiceVersionKey.String("1.0.0"),
		),
	)

	return res, err
}

func newTracerProvider() (*sdktrace.TracerProvider, error) {
	traceExporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, err
	}
	res, err := newResource()
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter, sdktrace.WithBatchTimeout(time.Second)),
		sdktrace.WithResource(res),
	)
	return tp, nil
}

func newMeterProvider() (*sdkmeter.MeterProvider, error) {
	metricExporter, err := stdoutmetric.New(stdoutmetric.WithPrettyPrint())
	if err != nil {
		return nil, err
	}

	res, err := newResource()
	if err != nil {
		return nil, err
	}
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
