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
)

func newServeCommand(flags *rootFlags, _ *viper.Viper) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the Limen HTTP server",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runServe(flags)
		},
	}
}

func runServe(flags *rootFlags) error {
	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()

	cfg, err := loadConfig(flags)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	d := &serverDeps{ctx: ctx, cfg: cfg, logger: logger}

	// Portal suite: cipher, storage, OIDC RP + /t/{tenant}/portal routes.
	cipher, store, signer, oidcHandler, err := setupPortal(d, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	d.cipher, d.store, d.signer, d.oidc = cipher, store, signer, oidcHandler

	// Upstream linking suite: build the strategy registry + Service before
	// mounting the portal so the portal PoC endpoints can consume it.
	upstreamCleanup, err := setupUpstreamLinking(d)
	if err != nil {
		return err
	}
	defer upstreamCleanup()

	// MCP suite: downstream gateway + MCP transport.
	gw, mcpServer := setupMCPGateway(d)

	// Compose router.
	r := chi.NewRouter()
	r.Use(permissiveCORS)
	r.Get("/", landingPage)
	mountPortal(r, d)
	if err := mountOAuthProxy(r, d); err != nil {
		return err
	}
	if err := mountMCPResource(r, d, mcpServer); err != nil {
		return err
	}
	mountUpstreamLinking(r, d)

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
}

func landingPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Limen</title><body style="font:14px system-ui;padding:2rem"><h1>Limen MCP Gateway</h1><p>Signed out. Sign in at <code>/t/&lt;tenant&gt;/auth/login</code>.</p></body>`))
}

// permissiveCORS reflects the request Origin and allows credentials, all
// headers, and all methods. Intended for development / early-stage
// deployments; tighten before production.
func permissiveCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		h := w.Header()
		if origin != "" {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Credentials", "true")
		} else {
			h.Set("Access-Control-Allow-Origin", "*")
		}
		if r.Method == http.MethodOptions {
			if v := r.Header.Get("Access-Control-Request-Headers"); v != "" {
				h.Set("Access-Control-Allow-Headers", v)
			} else {
				h.Set("Access-Control-Allow-Headers", "*")
			}
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
