// Package container provides the Dependency Injection (DI) logic for the Outbox Relay.
// It leverages uber-go/dig to assemble the application's components, including
// configuration, telemetry, storage drivers, and message publishers into a unified graph.
package container

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-outbox/relay/internal/config"
	"github.com/open-outbox/relay/internal/publishers"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/storage"
	"github.com/open-outbox/relay/internal/telemetry"
	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/compress"
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
		// Storage provider: selects and initializes the DB driver based on configuration.
		func(ctx context.Context, cfg *config.Config, tel telemetry.Telemetry) (relay.Storage, error) {
			switch cfg.StorageType {
			case "postgres":
				pool, err := pgxpool.New(ctx, cfg.StorageURL)
				if err != nil {
					return nil, err
				}
				postgres, err := storage.NewPostgres(pool, cfg.StorageTableName, cfg.RelayID, tel)
				if err != nil {
					return nil, err
				}
				return postgres, nil

			case "mysql":
				// return storage.NewMySQL(cfg.DatabaseURL), nil (To be implemented)
				return nil, fmt.Errorf("mysql storage is not implemented yet")

			default:
				return nil, fmt.Errorf("unknown storage type: %s", cfg.StorageType)
			}
		},
		// Publisher provider: selects and initializes the message broker driver based on configuration.
		func(cfg *config.Config) (relay.Publisher, error) {
			switch cfg.PublisherType {
			case "nats":
				return publishers.NewNats(
					cfg.PublisherURL,
					cfg.NatsPublishTimeout,
					cfg.NatsConnectionTimeout,
				)

			case "kafka":
				return buildKafkaPublisher(*cfg)

			case "redis":
				return publishers.NewRedis(
					cfg.PublisherURL,
					cfg.RedisConnectionTimeout,
					cfg.RedisWriteTimeout,
				)

			case "stdout":
				return publishers.NewStdout(), nil

			case "null":
				return publishers.NewNull(), nil

			default:
				return nil, fmt.Errorf("unknown publisher type: %s", cfg.PublisherType)
			}
		},
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
				Interval:                      cfg.PollInterval,
				BatchSize:                     cfg.BatchSize,
				LeaseTimeout:                  cfg.LeaseTimeout,
				ReapBatchSize:                 cfg.ReapBatchSize,
				PublisherConnectRetryInterval: cfg.PublisherConnectRetryInterval,
				HealthCheckInterval:           cfg.HealthCheckInterval,
				EnableStats:                   cfg.EnableStats,
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

// buildKafkaPublisher is a specialized factory that maps generic application configuration
// (strings) to Kafka-specific types like compression algorithms and required acknowledgment
// levels. This separation keeps the main container logic clean.
func buildKafkaPublisher(cfg config.Config) (relay.Publisher, error) {

	brokerList := strings.Split(strings.TrimPrefix(cfg.PublisherURL, "kafka://"), ",")

	if len(brokerList) < 1 || (len(brokerList) == 1 && brokerList[0] == "") {
		return nil, fmt.Errorf(
			"failed to create publisher: no broker addresses provided for kafka publisher",
		)
	}

	// Compression Mapping
	compressionMap := map[string]kafka.Compression{
		"gzip":   compress.Gzip,
		"snappy": compress.Snappy,
		"lz4":    compress.Lz4,
		"zstd":   compress.Zstd,
		"none":   compress.None,
	}

	comp, ok := compressionMap[strings.ToLower(cfg.KafkaCompression)]
	if !ok {
		return nil, fmt.Errorf("unsupported Kafka Compression type: %s", cfg.KafkaCompression)
	}

	// Required Acks Mapping
	// We allow both human-readable strings and common string-integers
	acksMap := map[string]kafka.RequiredAcks{
		"all":  kafka.RequireAll,  // -1
		"one":  kafka.RequireOne,  // 1
		"none": kafka.RequireNone, // 0
		"-1":   kafka.RequireAll,
		"1":    kafka.RequireOne,
		"0":    kafka.RequireNone,
	}

	acks, ok := acksMap[strings.ToLower(cfg.KafkaRequiredAcks)]

	if !ok {
		return nil, fmt.Errorf("unsupported Kafka RequiredAcks type: %s", cfg.KafkaRequiredAcks)
	}

	mechanism := strings.ToLower(strings.TrimSpace(cfg.KafkaSASLMechanism))
	if mechanism != "" {
		switch mechanism {
		case "plain", "scram-sha-256", "scram-sha-512":
		default:
			return nil, fmt.Errorf(
				"unsupported Kafka SASL Mechanism type: %s",
				cfg.KafkaSASLMechanism,
			)
		}
	}

	var tlsVersionMap = map[string]uint16{
		"1.0": tls.VersionTLS10,
		"1.1": tls.VersionTLS11,
		"1.2": tls.VersionTLS12,
		"1.3": tls.VersionTLS13,
	}

	tlsVersion := tls.VersionTLS12

	if version, exists := tlsVersionMap[cfg.KafkaTLSVersion]; exists {
		tlsVersion = int(version)
	}

	kCfg := publishers.KafkaConfig{
		Brokers:           brokerList,
		MaxAttempts:       cfg.KafkaMaxAttempts,
		WriteTimeout:      cfg.KafkaWriteTimeout,
		ReadTimeout:       cfg.KafkaReadTimeout,
		ConnectionTimeout: cfg.KafkaConnectionTimeout,
		BatchSize:         cfg.KafkaBatchSize,
		BatchBytes:        cfg.KafkaBatchBytes,
		BatchTimeout:      cfg.KafkaBatchTimeout,
		Async:             cfg.KafkaAsync,
		Compression:       comp,
		RequiredAcks:      acks,
		TLSCA:             cfg.KafkaTLSCA,
		TLSCert:           cfg.KafkaTLSCert,
		TLSKey:            cfg.KafkaTLSKey,
		TLSVersion:        uint16(tlsVersion),
		ServerName:        cfg.KafkaServerName,
		Insecure:          cfg.KafkaInsecure,
		SASLMechanism:     cfg.KafkaSASLMechanism,
		Username:          cfg.KafkaUsername,
		Password:          cfg.KafkaPassword,
		IdleTimeout:       cfg.KafkaIdleTimeout,
		KeepAlive:         cfg.KafkaKeepAlive,
	}
	return publishers.NewKafka(kCfg)
}
