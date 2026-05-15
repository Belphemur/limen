// Package cli — outbound upstream linking suite.
//
// Owns the wiring for the per-user link strategies (none /
// static_header / mcp_spec), the Valkey-backed one-shot OAuth state,
// the /callback HTTP route every strategy redirects through, and the
// background token refresher goroutine.
package cli

import (
	"fmt"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/transport"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/mcpspec"
	"github.com/belphemur/limen/internal/upstream/none"
	"github.com/belphemur/limen/internal/upstream/oauthstate"
	"github.com/belphemur/limen/internal/upstream/statichdr"
	"github.com/belphemur/limen/internal/valkey"
)

// mountUpstreamLinking wires the upstream linking suite: Valkey-backed
// OAuth state, the strategy registry, the /callback HTTP route, and
// the background token refresher.
//
// Disabled (with a warn log) when valkey.address is empty so early-stage
// dev configs that haven't stood up Valkey yet still boot. Returns a
// cleanup func that closes the Valkey client; the caller defers it. The
// cleanup is a no-op when the suite is disabled.
func mountUpstreamLinking(r chi.Router, d *serverDeps) (cleanup func(), err error) {
	cleanup = func() {}
	if d.cfg.Valkey.Address == "" {
		d.logger.Warn("valkey.address empty: upstream linking disabled")
		return cleanup, nil
	}

	vk, vkErr := valkey.Open(d.cfg.Valkey)
	if vkErr != nil {
		return cleanup, fmt.Errorf("open valkey: %w", vkErr)
	}
	cleanup = vk.Close

	stateStore := oauthstate.New(vk, d.cipher, oauthstate.DefaultTTL)

	registry := upstream.NewRegistry()
	registry.Register(none.New(nil))
	registry.Register(statichdr.New(d.store, d.cipher, nil))

	mcpStrat, msErr := mcpspec.New(d.store, d.cipher, stateStore, mcpspec.Options{
		RedirectURLFn: func(tenantPublic, upstreamName string) string {
			return d.cfg.Server.BaseURL + "/t/" + tenantPublic + "/upstream/" + upstreamName + "/callback"
		},
		ProactiveWindow: d.cfg.UpstreamRefresh.ProactiveWindow,
		SoftwareID:      "limen-gateway",
	})
	if msErr != nil {
		return cleanup, fmt.Errorf("build mcpspec strategy: %w", msErr)
	}
	registry.Register(mcpStrat)

	svc := upstream.NewService(d.store, registry)
	transport.MountUpstream(r, transport.UpstreamDeps{
		Store:   d.store,
		OIDC:    d.oidc,
		Service: svc,
		Logger:  d.logger,
	})

	refresher := upstream.NewRefresher(d.store, registry, upstream.RefresherOptions{
		Interval:      d.cfg.UpstreamRefresh.Interval,
		RefreshWindow: d.cfg.UpstreamRefresh.RefreshWindow,
		HealthThresholds: upstream.HealthThresholds{
			FailThreshold:     d.cfg.UpstreamRefresh.FailThreshold,
			FailWindow:        d.cfg.UpstreamRefresh.FailWindow,
			NeedsRelinkWindow: d.cfg.UpstreamRefresh.NeedsRelinkWindow,
		},
		Logger: d.logger,
	})
	go refresher.Run(d.ctx)
	d.logger.Info("upstream refresher started",
		zap.Duration("interval", d.cfg.UpstreamRefresh.Interval),
		zap.Duration("refresh_window", d.cfg.UpstreamRefresh.RefreshWindow))
	return cleanup, nil
}
