package container

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-outbox/relay/internal/config"
	"github.com/open-outbox/relay/internal/publishers"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/storage"
	"go.uber.org/dig"
	"go.uber.org/zap"
)
func BuildContainer() *dig.Container {
	c := dig.New()

	// Provide Config
	c.Provide(config.Load)

	c.Provide(func(cfg *config.Config) (*zap.Logger, error) {
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
	})

	// Storage Provider
	c.Provide(func(cfg *config.Config) (relay.Storage, error) {
		ctx := context.Background()

		fmt.Printf("This is the type %s\n", cfg.PublisherURL)
		
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
	})

	// Publisher Provider
	c.Provide(func(cfg *config.Config) (relay.Publisher, error) {
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
			
		default:
			return nil, fmt.Errorf("unknown publisher type: %s", cfg.PublisherType)
		}
	})

	
	// Provide Engine
	c.Provide(func(s relay.Storage, p relay.Publisher, cfg *config.Config) *relay.Engine {
		return relay.NewEngine(s, p, cfg.PollInterval)
	})

		
	// Provide API Server
	c.Provide(func(s relay.Storage, cfg *config.Config) *relay.Server {
		return relay.NewServer(s, cfg.ServerPort)
	})

	return c
}
