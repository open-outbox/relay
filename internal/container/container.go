package container

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
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

func BuildContainer(rootCtx context.Context) (*dig.Container, error) {
	c := dig.New()

	dependencies := []interface{}{
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
				Logger:         logger,
				Metrics:        metrics,
				TracerProvider: tp,
				MeterProvider:  mp,
			}
		},
		func(ctx context.Context, cfg *config.Config) (relay.Storage, error) {

			switch cfg.StorageType {
			case "postgres":
				pool, err := pgxpool.New(ctx, cfg.StorageURL)
				if err != nil {
					return nil, err
				}
				return storage.NewPostgres(pool), nil

			case "mysql":
				// return storage.NewMySQL(cfg.DatabaseURL), nil (To be implemented)
				return nil, fmt.Errorf("mysql storage not yet implemented")

			default:
				return nil, fmt.Errorf("unknown storage type: %s", cfg.StorageType)
			}
		},
		func(cfg *config.Config) (relay.Publisher, error) {
			switch cfg.PublisherType {
			case "nats":
				return publishers.NewNats(cfg.PublisherURL)

			case "kafka":
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
		func(
			s relay.Storage,
			p relay.Publisher,
			cfg *config.Config,
			tel telemetry.Telemetry,
		) *relay.Engine {

			retruPolicy := relay.ExponentialBackoff{
				MaxAttempts: cfg.RetryMaxAttempts,
				BaseDelay:   cfg.RetryBaseDelay,
				MaxDelay:    cfg.RetryMaxDelay,
				Jitter:      cfg.RetryJitter,
			}

			params := relay.EngineParams{
				RelayID:       cfg.RELAY_ID,
				Interval:      cfg.PollInterval,
				BatchSize:     cfg.BatchSize,
				LeaseTimeout:  cfg.LeaseTimeout,
				ReapBatchSize: cfg.ReapBatchSize,
				RetryPolicy:   retruPolicy,
			}

			return relay.NewEngine(s, p, params, tel)
		},
		func(ctx context.Context, s relay.Storage, cfg *config.Config, logger *zap.Logger) *relay.Server {
			return relay.NewServer(ctx, s, cfg.ServerPort, logger)
		},
	}

	for _, dependency := range dependencies {
		if err := c.Provide(dependency); err != nil {
			return nil, fmt.Errorf("failed to provide %T: %w", dependency, err)
		}
	}

	return c, nil

}
