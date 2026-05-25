// Package boot's http.go holds HTTP-server primitives shared by every
// binary: graceful-shutdown wrapper, health endpoints, landing page,
// permissive dev CORS.
package boot

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

type ctxKeyType struct{}

var ctxKeyLogger ctxKeyType

// RunHTTPServer binds the configured listener and shuts down cleanly
// when rt.Ctx is canceled.
func RunHTTPServer(rt *Runtime, h http.Handler) error {
	addr := fmt.Sprintf("%s:%d", rt.Cfg.Server.Host, rt.Cfg.Server.Port)
	rt.Logger.Info("starting server", zap.String("addr", addr))

	srv := &http.Server{Addr: addr, Handler: h}
	srv.ReadTimeout = rt.Cfg.Server.ReadTimeout
	srv.ReadHeaderTimeout = max(rt.Cfg.Server.ReadTimeout/2, 10*time.Second)
	srv.WriteTimeout = rt.Cfg.Server.WriteTimeout
	srv.IdleTimeout = rt.Cfg.Server.IdleTimeout
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("server failed: %w", err)
		}
		return nil
	case <-rt.Ctx.Done():
		rt.Logger.Info("shutting down server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), rt.Cfg.Server.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			rt.Logger.Warn("server shutdown timed out", zap.Error(err))
		} else {
			rt.Logger.Info("server stopped")
		}
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

// RequestLogger injects request_id, method, and path into a per-request
// logger stored on the context. Must be mounted AFTER middleware.RequestID.
func RequestLogger(logger *zap.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := middleware.GetReqID(r.Context())
			l := logger.With(
				zap.String("request_id", reqID),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
			)
			ctx := context.WithValue(r.Context(), ctxKeyLogger, l)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// LoggerFromContext returns the per-request logger or a no-op fallback.
func LoggerFromContext(ctx context.Context) *zap.Logger {
	if l, ok := ctx.Value(ctxKeyLogger).(*zap.Logger); ok {
		return l
	}
	return zap.NewNop()
}
