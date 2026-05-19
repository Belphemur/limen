// Package boot's http.go holds HTTP-server primitives shared by every
// binary: graceful-shutdown wrapper, health endpoints, landing page,
// permissive dev CORS.
package boot

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// RunHTTPServer binds the configured listener and shuts down cleanly
// when rt.Ctx is canceled.
func RunHTTPServer(rt *Runtime, h http.Handler) error {
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

// MountHealth attaches /healthz + /readyz handlers. Liveness only —
// readiness is a future hook for dependency probes.
func MountHealth(r chi.Router) {
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// LandingPage is the GET / handler used by the all-in-one binary.
func LandingPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Limen</title><body style="font:14px system-ui;padding:2rem"><h1>Limen MCP Gateway</h1><p>Signed out. Sign in at <code>/t/&lt;tenant&gt;/auth/login</code>.</p></body>`))
}

// PermissiveCORS reflects the request Origin and allows credentials, all
// headers, and all methods. Intended for development / early-stage
// deployments; tighten before production.
func PermissiveCORS(next http.Handler) http.Handler {
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
