// Package cli — `limen serve` (all-in-one) entry path.
//
// Boots the union of every suite via BootRuntime(AllProfiles) and
// mounts every route the split binaries (cmd/gateway, cmd/portal,
// cmd/staff) collectively expose. This is the lowest-friction
// self-hosted deployment shape.
package cli

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func newServeCommand(flags *rootFlags, _ *viper.Viper) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the Limen HTTP server (all-in-one)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runServe(flags)
		},
	}
}

// runServe is the all-in-one entry point. It boots every dependency
// and mounts every route. Used by the `limen serve` subcommand and by
// cmd/limen/main.go.
func runServe(flags *rootFlags) error {
	rt, cleanup, err := BootRuntime(flags, AllProfiles)
	if err != nil {
		return err
	}
	defer cleanup()

	_, mcpServer, err := setupMCPGateway(rt)
	if err != nil {
		return err
	}

	r := chi.NewRouter()
	r.Use(permissiveCORS)
	r.Get("/", landingPage)
	mountHealth(r)
	mountPortal(r, rt)
	if err := mountOAuthProxy(r, rt); err != nil {
		return err
	}
	if err := mountMCPResource(r, rt, mcpServer); err != nil {
		return err
	}
	mountUpstreamLinking(r, rt)

	return runHTTPServer(rt, r)
}

// runHTTPServer binds the configured listener and shuts down cleanly
// when rt.Ctx is canceled. Shared by every service binary.
func runHTTPServer(rt *Runtime, h http.Handler) error {
	addr := fmt.Sprintf("%s:%d", rt.Cfg.Server.Host, rt.Cfg.Server.Port)
	rt.Logger.Info("starting server", zap.String("addr", addr))

	srv := &http.Server{Addr: addr, Handler: h}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("server failed: %w", err)
		}
		return nil
	case <-rt.Ctx.Done():
		shutdownCtx, c := context.WithCancel(context.Background())
		c()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	}
}

// mountHealth attaches /healthz + /readyz handlers. Liveness only —
// readiness is a future hook for dependency probes.
func mountHealth(r chi.Router) {
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// buildServeLogger constructs the zap logger used by every service
// binary. level accepts the standard zapcore names (debug, info, warn,
// error, dpanic, panic, fatal); empty falls back to "info". When
// development is true the human-readable development encoder is used;
// otherwise the JSON production encoder is used.
func buildServeLogger(level string, development bool) (*zap.Logger, error) {
	var cfg zap.Config
	if development {
		cfg = zap.NewDevelopmentConfig()
	} else {
		cfg = zap.NewProductionConfig()
	}
	if level == "" {
		level = "info"
	}
	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("parse log level %q: %w", level, err)
	}
	cfg.Level = zap.NewAtomicLevelAt(lvl)
	return cfg.Build()
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
