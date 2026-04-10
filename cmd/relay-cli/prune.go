package main

import (
	"fmt"
	"regexp"

	"github.com/open-outbox/relay/internal/relay"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var (
	deliveredAge string
	deadAge      string
	dryRun       bool
)

var pruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Cleanup old events from the outbox",
	Long:  `Permanently removes events with status DELIVERED or DEAD based on retention duration.`,
	RunE: func(cmd *cobra.Command, _ []string) error {

		if deliveredAge == "" && deadAge == "" {
			return fmt.Errorf("must provide at least one of --delivered-age or --dead-age")
		}

		re := regexp.MustCompile(`^\d+[dhm]$`)
		if deliveredAge != "" && !re.MatchString(deliveredAge) {
			return fmt.Errorf(
				"invalid --delivered-age format '%s': must be number followed by d, h, or m (e.g., 7d)",
				deliveredAge,
			)
		}
		if deadAge != "" && !re.MatchString(deadAge) {
			return fmt.Errorf(
				"invalid --dead-age format '%s': must be number followed by d, h, or m (e.g., 30d)",
				deadAge,
			)
		}

		return di.Invoke(func(s relay.Storage, logger *zap.Logger) error {
			defer func() { _ = logger.Sync() }()
			defer func() { _ = s.Close() }()

			logger.Info("starting prune operation",
				zap.String("delivered_age", deliveredAge),
				zap.String("dead_age", deadAge),
				zap.Bool("dry_run", dryRun),
			)

			res, err := s.Prune(cmd.Context(), relay.PruneOptions{
				DeliveredAge: deliveredAge,
				DeadAge:      deadAge,
				DryRun:       dryRun,
			})

			if err != nil {
				return err
			}

			logger.Info("Prune report",
				zap.Int64("delivered events", res.DeliveredDeleted),
				zap.Int64("dead events", res.DeadDeleted),
				zap.Bool("applied", !dryRun),
			)
			logger.Info("prune operation completed successfully")
			return nil
		})
	},
}

func init() {
	pruneCmd.Flags().
		StringVar(&deliveredAge, "delivered-age", "7d", "Retention period for DELIVERED events")
	pruneCmd.Flags().StringVar(&deadAge, "dead-age", "30d", "Retention period for DEAD events")
	pruneCmd.Flags().
		BoolVar(&dryRun, "dry-run", false, "Calculate and log deletions without executing them")

	rootCmd.AddCommand(pruneCmd)
}
