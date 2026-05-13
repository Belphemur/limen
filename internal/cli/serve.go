package cli

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/transport"
)

func newServeCommand(flags *rootFlags, _ *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Limen HTTP server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger, _ := zap.NewProduction()
			defer func() { _ = logger.Sync() }()

			cfg, err := loadConfig(flags)
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			gw := gateway.New(logger)

			for _, uc := range cfg.Upstreams {
				client := gateway.NewMCPUpstream(uc.Name, uc.URL, uc.Headers, uc.Timeout, logger)
				if err := client.Connect(ctx); err != nil {
					logger.Error("failed to connect upstream",
						zap.String("name", uc.Name),
						zap.Error(err))
					continue
				}
				if err := gw.AddUpstream(ctx, client); err != nil {
					logger.Error("failed to add upstream",
						zap.String("name", uc.Name),
						zap.Error(err))
					_ = client.Close()
					continue
				}
			}

			if len(gw.UpstreamNames()) == 0 {
				return fmt.Errorf("no upstreams connected")
			}

			handler := gateway.NewCodeModeHandler(gw, logger, cfg.CodeMode.ExecutionTimeout)
			mcpServer := transport.NewMCPServer(gw, handler, logger)

			addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
			logger.Info("starting gateway",
				zap.String("addr", addr),
				zap.Int("upstreams", len(gw.UpstreamNames())),
				zap.Strings("upstream_names", gw.UpstreamNames()))

			if err := mcpServer.Start(ctx, addr); err != nil && err != context.Canceled {
				return fmt.Errorf("server failed: %w", err)
			}
			return nil
		},
	}
	return cmd
}
