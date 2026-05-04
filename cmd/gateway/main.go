package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/transport"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	logger, _ := zap.NewProduction()
	defer logger.Sync()

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gw := gateway.New(logger)

	for _, uc := range cfg.Upstreams {
		client := gateway.NewMCPUpstream(
			uc.Name,
			uc.URL,
			uc.Headers,
			uc.Timeout,
			logger,
		)

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
			client.Close()
			continue
		}
	}

	if len(gw.UpstreamNames()) == 0 {
		logger.Fatal("no upstreams connected")
	}

	handler := gateway.NewCodeModeHandler(gw, logger, cfg.CodeMode.ExecutionTimeout)

	mcpServer := transport.NewMCPServer(gw, handler, logger)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	logger.Info("starting gateway",
		zap.String("addr", addr),
		zap.Int("upstreams", len(gw.UpstreamNames())),
		zap.Strings("upstream_names", gw.UpstreamNames()))

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down")
		cancel()
	}()

	if err := mcpServer.Start(ctx, addr); err != nil && err != context.Canceled {
		logger.Fatal("server failed", zap.Error(err))
	}
}
