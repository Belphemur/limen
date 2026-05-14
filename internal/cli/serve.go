package cli

import (
	"context"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/transport"
	"github.com/belphemur/limen/internal/zitadel"
)

func newServeCommand(flags *rootFlags, _ *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Limen HTTP server",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			logger, _ := zap.NewProduction()
			defer func() { _ = logger.Sync() }()

			cfg, err := loadConfig(flags)
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			// crypto + storage
			key, err := crypto.ParseKey(cfg.Security.TokenEncryptionKey)
			if err != nil {
				return fmt.Errorf("parse token encryption key: %w", err)
			}
			cipher, err := crypto.NewCipher(key)
			if err != nil {
				return fmt.Errorf("build cipher: %w", err)
			}
			crypto.SetCipher(cipher)

			store, err := storage.Open(cfg.Database)
			if err != nil {
				return fmt.Errorf("open storage: %w", err)
			}
			defer func() { _ = store.Close() }()

			signer, err := auth.NewStateSigner(key[:])
			if err != nil {
				return fmt.Errorf("build state signer: %w", err)
			}
			oidcHandler, err := auth.NewOIDC(ctx, auth.OIDCConfig{
				Issuer:       cfg.OIDC.Issuer,
				ClientID:     cfg.OIDC.ClientID,
				ClientSecret: cfg.OIDC.ClientSecret,
				RedirectURI:  cfg.OIDC.RedirectURI,
				Scopes:       cfg.OIDC.Scopes,
				Secure:       cfg.Security.PortalSessionCookieSecure,
			}, cipher, signer, logger)
			if err != nil {
				return fmt.Errorf("build oidc handler: %w", err)
			}

			// gateway + upstreams (best-effort; portal is still useful w/o them)
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
			cmHandler := gateway.NewCodeModeHandler(gw, logger, cfg.CodeMode.ExecutionTimeout)
			mcpServer := transport.NewMCPServer(gw, cmHandler, logger)

			// compose router
			r := chi.NewRouter()
			r.Get("/", func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Limen</title><body style="font:14px system-ui;padding:2rem"><h1>Limen MCP Gateway</h1><p>Signed out. Sign in at <code>/t/&lt;tenant&gt;/auth/login</code>.</p></body>`))
			})
			transport.MountPortal(r, transport.PortalDeps{
				Store:                 store,
				OIDC:                  oidcHandler,
				Logger:                logger,
				PostLogoutRedirectURI: cfg.OIDC.PostLogoutRedirectURI,
			})

			// Phase 5: AS-metadata + DCR proxy + redirector under /t/{tenant}/oauth/*.
			// Best-effort: if the Zitadel admin client can't be built (dev without
			// PAT, missing project id, etc.) skip the proxy and continue with portal
			// + MCP only.
			zclient, zerr := zitadel.NewClient(ctx, zitadel.Config{
				Domain:      cfg.Zitadel.Domain,
				AuthMode:    zitadel.AuthMode(cfg.Zitadel.AuthMode),
				PAT:         cfg.Zitadel.PAT,
				JWTKeyPath:  cfg.Zitadel.JWTKeyPath,
				ProjectID:   cfg.Zitadel.ProjectID,
				HTTPTimeout: cfg.Zitadel.HTTPTimeout,
			})
			if zerr != nil {
				logger.Warn("oauth proxy disabled: zitadel admin client unavailable", zap.Error(zerr))
			} else if err := transport.MountOAuthProxy(r, transport.OAuthProxyDeps{
				Store:    store,
				Zitadel:  zclient,
				Logger:   logger,
				BaseURL:  cfg.Server.BaseURL,
				Issuer:   cfg.OIDC.Issuer,
				OAuthCfg: cfg.OAuthProxy,
			}); err != nil {
				return fmt.Errorf("mount oauth proxy: %w", err)
			}

			mcpServer.Mount(r)

			addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
			logger.Info("starting gateway",
				zap.String("addr", addr),
				zap.Int("upstreams", len(gw.UpstreamNames())),
				zap.Strings("upstream_names", gw.UpstreamNames()))

			srv := &http.Server{Addr: addr, Handler: r}
			errCh := make(chan error, 1)
			go func() { errCh <- srv.ListenAndServe() }()
			select {
			case err := <-errCh:
				if err != nil && err != http.ErrServerClosed {
					return fmt.Errorf("server failed: %w", err)
				}
				return nil
			case <-ctx.Done():
				shutdownCtx, c := context.WithCancel(context.Background())
				c()
				_ = srv.Shutdown(shutdownCtx)
				return nil
			}
		},
	}
	return cmd
}
