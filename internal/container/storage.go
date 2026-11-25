package container

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-outbox/relay/internal/config"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/open-outbox/relay/internal/storage"
	"github.com/open-outbox/relay/internal/telemetry"
)

func buildStorage(
	ctx context.Context,
	cfg *config.Config,
	tel telemetry.Telemetry,
) (relay.Storage, error) {
	switch cfg.StorageType {
	case "postgres":
		pool, err := pgxpool.New(ctx, cfg.StorageURL)
		if err != nil {
			return nil, err
		}
		return storage.NewPostgres(pool, cfg.StorageTableName, cfg.RelayID, tel)

	case "mysql":
		return nil, fmt.Errorf("mysql storage is not implemented yet")

	default:
		return nil, fmt.Errorf("unknown storage type: %s", cfg.StorageType)
	}
}
