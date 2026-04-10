package main

import (
	"context"
	"fmt"

	"github.com/open-outbox/relay/internal/config"
	"github.com/open-outbox/relay/internal/container"
	"github.com/spf13/cobra"
	"go.uber.org/dig"
)

var (
	di *dig.Container
)

var rootCmd = &cobra.Command{
	Use:   "relay-cli",
	Short: "Management tool for Open Outbox Relay",
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		var err error
		di, err = container.BuildContainer(cmd.Context())
		if err != nil {
			return fmt.Errorf("failed to build container: %w", err)
		}

		dbURL, _ := cmd.Flags().GetString("storage-url")
		if dbURL != "" {
			return di.Invoke(func(cfg *config.Config) {
				cfg.StorageURL = dbURL
			})
		}
		return nil
	},
}

func Execute(ctx context.Context) error {
	return rootCmd.ExecuteContext(ctx)
}

func init() {
	rootCmd.PersistentFlags().
		String("storage-url", "", "Database connection URL (overrides STORAGE_URL)")
}
