// Package container provides the Dependency Injection (DI) logic for the Outbox Relay.
// It leverages uber-go/dig to assemble the application's components, including
// configuration, telemetry, storage drivers, and message publishers into a unified graph.
package container

import (
	"context"
	"fmt"

	"github.com/open-outbox/relay/internal/config"
	"github.com/open-outbox/relay/internal/publishers"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/storage"
	"github.com/open-outbox/relay/internal/telemetry"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/dig"
	"go.uber.org/zap"
)

// BuildContainer initializes and returns a dig.Container with all application dependencies wired.
// It handles the construction of the logger, telemetry providers, database storage,
// and the message publisher based on the loaded configuration.
func BuildContainer(rootCtx context.Context) (*dig.Container, error) {
	c := dig.New()

	dependencies := []interface{}{
		// Provide the root context to the container.
		func() context.Context {
			return rootCtx
		},
		config.Load,
		telemetry.NewMetrics,
		telemetry.NewOTelProviders,
		func(p *telemetry.OTelProviders) trace.TracerProvider { return p.TraceProvider },
		func(p *telemetry.OTelProviders) metric.MeterProvider { return p.MeterProvider },
		func(cfg *config.Config) (*zap.Logger, error) {
			var logger *zap.Logger
			var err error
			if cfg.Environment == config.Production {
				logger, err = zap.NewProduction()
			} else {
				logger, err = zap.NewDevelopment()
			}

			if err != nil {
				return nil, err
			}
			return logger, nil
		},
		func(
			logger *zap.Logger,
			metrics *telemetry.Metrics,
			tp trace.TracerProvider,
			mp metric.MeterProvider,
		) telemetry.Telemetry {
			return telemetry.Telemetry{
				Logger:  logger,
				Metrics: metrics,
				Tracer:  tp.Tracer(telemetry.InstrumentationName),
				Meter:   mp.Meter(telemetry.InstrumentationName),
			}
		},
		buildStorage,
		buildPublisher,
		// Relay Engine: wires the core processing logic with its required storage and publisher.
		func(
			s relay.Storage,
			p relay.Publisher,
			cfg *config.Config,
			tel telemetry.Telemetry,
		) (*relay.Engine, error) {

			retruPolicy := relay.ExponentialBackoff{
				MaxAttempts: cfg.RetryMaxAttempts,
				BaseDelay:   cfg.RetryBaseDelay,
				MaxDelay:    cfg.RetryMaxDelay,
				Jitter:      cfg.RetryJitter,
			}

			params := relay.EngineParams{
				RelayID:                       cfg.RelayID,
				EngineMode:                    relay.EngineMode(cfg.EngineMode),
				Interval:                      cfg.PollInterval,
				BatchSize:                     cfg.BatchSize,
				LeaseTimeout:                  cfg.LeaseTimeout,
				ReapBatchSize:                 cfg.ReapBatchSize,
				PublisherConnectRetryInterval: cfg.PublisherConnectRetryInterval,
				HealthCheckInterval:           cfg.HealthCheckInterval,
				ShutdownTimeout:               cfg.ShutdownTimeout,
				EnableStats:                   cfg.EnableStats,
				EnableReaper:                  cfg.EnableReaper,
				RetryPolicy:                   retruPolicy,
			}

			instrumentedPublisher := publishers.NewInstrumented(p, tel, cfg.RelayID)
			instrumentedStorage := storage.NewInstrumented(s, tel, cfg.RelayID)

			return relay.NewEngine(instrumentedStorage, instrumentedPublisher, params, tel)
		},
		// Server provider: provides the HTTP server used for health checks and Prometheus metrics.
		func(
			ctx context.Context,
			s relay.Storage,
			p relay.Publisher,
			cfg *config.Config,
			logger *zap.Logger,
		) *relay.Server {
			return relay.NewServer(ctx, s, p, cfg.ServerPort, logger)
		},
	}

	for _, dependency := range dependencies {
		if err := c.Provide(dependency); err != nil {
			return nil, fmt.Errorf("failed to provide %T: %w", dependency, err)
		}
	}

	return c, nil
}
