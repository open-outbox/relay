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
)
func BuildContainer() *dig.Container {
	c := dig.New()

	// Provide Config
	c.Provide(config.Load)

	// Storage Provider
	c.Provide(func(cfg *config.Config) (relay.Storage, error) {
		ctx := context.Background()
		
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
			return nil, fmt.Errorf("kafka publisher not yet implemented")
			
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
