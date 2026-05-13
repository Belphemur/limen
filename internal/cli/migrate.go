package cli

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/belphemur/limen/internal/storage"
)

func newMigrateCommand(flags *rootFlags, _ *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Run database migrations",
		Long: `Run AutoMigrate and the embedded SQL migrations against the
configured Postgres instance. Idempotent. Safe to run on startup or out of
band.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger := newCLILogger()
			defer func() { _ = logger.Sync() }()

			cfg, err := loadConfig(flags)
			if err != nil {
				return err
			}

			store, err := storage.Open(cfg.Database)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			logger.Info("running migrations")
			if err := store.Migrate(ctx); err != nil {
				return err
			}
			logger.Info("migrations complete")
			return nil
		},
	}
	return cmd
}
