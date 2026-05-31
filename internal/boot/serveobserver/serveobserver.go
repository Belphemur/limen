// Package serveobserver is the metrics consumer binary entry point used by
// cmd/observer. It bootstraps a background metrics consumer and serves /healthz + /readyz.
package serveobserver

import (
	"fmt"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/belphemur/limen/internal/billing/metrics"
	"github.com/belphemur/limen/internal/boot"
)

// Run boots a runtime + background metrics consumer and serves healthz until SIGINT/SIGTERM.
func Run(configPath string) error {
	rt, cleanup, err := boot.BootRuntime(configPath, boot.NeedStore|boot.NeedCipher|boot.NeedUpstream)
	if err != nil {
		return err
	}
	defer cleanup()

	// Start billing metrics consumer if Valkey is enabled
	if rt.Valkey == nil {
		return fmt.Errorf("valkey is required for metrics observer")
	}

	consumer := metrics.NewConsumer(rt.Valkey, rt.Store, rt.Logger.Named("billing-consumer"), "limen-observer")
	go consumer.Run(rt.Ctx)

	r := chi.NewRouter()
	r.Use(boot.PermissiveCORS)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(boot.RequestLogger(rt.Logger))
	boot.MountHealth(r)

	return boot.RunHTTPServer(rt, r)
}
