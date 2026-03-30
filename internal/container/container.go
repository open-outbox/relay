package container

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-outbox/relay/internal/config"
	"github.com/open-outbox/relay/internal/publishers"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/storage"
	prometheus_client "github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	oteltrace "go.opentelemetry.io/otel/trace" // Alias the API
	"go.uber.org/dig"
	"go.uber.org/zap"
)

func BuildContainer(rootCtx context.Context) *dig.Container {
	c := dig.New()

	dependencies := []interface{}{
		func() context.Context {
			return rootCtx
		},
		config.Load,
		relay.NewMetrics,
		func(cfg *config.Config) (*zap.Logger, error) {
			var logger *zap.Logger
			var err error

			// Switch based on environment logic if you want
			// Development: Pretty-printed, colorized
			// Production: Mini-JSON, high performance
			if cfg.Environment == "production" {
				logger, err = zap.NewProduction()
			} else {
				logger, err = zap.NewDevelopment()
			}

			if err != nil {
				return nil, err
			}
			return logger, nil
		},

		// Inside BuildContainer...
		func() (oteltrace.Tracer, error) {
			// 1. Create an exporter (sending to stdout for now)
			exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
			if err != nil {
				return nil, err
			}

			// 2. Define the resource (service name)
			res, _ := resource.Merge(
				resource.Default(),
				resource.NewWithAttributes(
					semconv.SchemaURL,
					semconv.ServiceNameKey.String("outbox-relay"),
				),
			)

			// 3. Create the Provider
			tp := trace.NewTracerProvider(
				trace.WithBatcher(exporter),
				trace.WithResource(res),
			)

			// Set global provider so libraries can use it
			otel.SetTracerProvider(tp)

			return tp.Tracer("outbox-relay-engine"), nil
		},

		func() (metric.Meter, error) {
			// 1. Create the Prometheus exporter
			// 1. Tell OTEL to use the Global Prometheus Registerer
			// This is the "magic link" that connects OTEL to promhttp.Handler()
			exporter, err := prometheus.New(
				prometheus.WithRegisterer(prometheus_client.DefaultRegisterer),
			)
			if err != nil {
				return nil, err
			}

			// 2. Setup the Provider
			provider := sdkmetric.NewMeterProvider(
				sdkmetric.WithReader(exporter),
			)

			// 3. Set this as the GLOBAL Meter Provider (Optional but recommended)
			otel.SetMeterProvider(provider)

			return provider.Meter("open-outbox-relay"), nil
		},

		// Storage Provider
		func(ctx context.Context, cfg *config.Config) (relay.Storage, error) {

			switch cfg.StorageType {
			case "postgres":
				pool, err := pgxpool.New(ctx, cfg.StorageURL)
				if err != nil {
					return nil, err
				}
				return storage.NewPostgres(pool), nil

			case "memory":
				return storage.NewMemory(), nil

			case "mysql":
				// return storage.NewMySQL(cfg.DatabaseURL), nil (To be implemented)
				return nil, fmt.Errorf("mysql storage not yet implemented")

			default:
				return nil, fmt.Errorf("unknown storage type: %s", cfg.StorageType)
			}
		},

		// Publisher Provider
		func(cfg *config.Config) (relay.Publisher, error) {
			switch cfg.PublisherType {
			case "nats":
				return publishers.NewNats(cfg.PublisherURL)

			case "kafka":
				// return publishers.NewKafka(cfg.KafkaBrokers), nil (To be implemented)
				return publishers.NewKafka(cfg.PublisherURL), nil

			case "redis":
				return publishers.NewRedis(cfg.PublisherURL)

			case "stdout":
				return publishers.NewStdout(), nil

			case "null":
				return publishers.NewNull(), nil

			default:
				return nil, fmt.Errorf("unknown publisher type: %s", cfg.PublisherType)
			}
		},

		// Provide Engine
		func(s relay.Storage, p relay.Publisher, cfg *config.Config, logger *zap.Logger, metrics *relay.Metrics, tracer oteltrace.Tracer) *relay.Engine {
			return relay.NewEngine(s, p, cfg.PollInterval, cfg.BatchSize, logger, metrics, tracer)
		},

		// Provide API Server
		func(s relay.Storage, cfg *config.Config, logger *zap.Logger) *relay.Server {
			return relay.NewServer(s, cfg.ServerPort, logger)
		}}

	for _, dependency := range dependencies {
		if err := c.Provide(dependency); err != nil {
			log.Fatalf("error in providing dependency: %v\n", err)
		}
	}

	return c
}
