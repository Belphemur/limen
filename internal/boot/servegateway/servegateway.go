// Package servegateway is the MCP RS hot-path binary entry point used
// by cmd/gateway. It mounts ONLY /t/{tenant}/mcp/* + /healthz + /readyz.
//
// Phase 9a load-bearing constraint: this package and its transitive
// import graph must NOT include internal/oauthproxy or
// internal/zitadel. The MCP gateway is the most internet-exposed,
// highest-traffic binary; it must not carry the credential surface of
// the DCR proxy or the Zitadel management client.
package servegateway

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/billing/enforcer"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/boot/mcpmount"
	"github.com/belphemur/limen/internal/mcprs"
)

// Run boots a runtime + MCP-only mux and serves until SIGINT/SIGTERM.
func Run(configPath string) error {
	rt, cleanup, err := boot.BootRuntime(configPath, boot.NeedStore|boot.NeedCipher|boot.NeedUpstream)
	if err != nil {
		return err
	}
	defer cleanup()

	r := chi.NewRouter()
	r.Use(boot.PermissiveCORS)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(boot.RequestLogger(rt.Logger))
	boot.MountHealth(r)

	_, mcpServer, err := mcpmount.Build(rt, nil)
	if err != nil {
		return err
	}
	metadataHandler, err := mcprs.NewHandler(mcprs.MetadataConfig{BaseURL: rt.Cfg.Server.BaseURL})
	if err != nil {
		return err
	}
	mcpAuth, err := auth.NewMCPAuth(rt.Ctx, auth.MCPAuthConfig{
		Issuer:   rt.Cfg.OIDC.Issuer,
		Audience: rt.Cfg.Zitadel.MCPResourceAudience,
	}, metadataHandler, rt.Store, rt.Logger)
	if err != nil {
		return err
	}
	// The gateway binary doesn't import billingmount (that pulls the
	// Stripe service and its portal dependency — see cmd/gateway's
	// import-graph test). We still need a live Enforcer here so the
	// lifecycle middleware can invalidate the entitlement cache after
	// a one-time auto-downgrade for cancelled/expired-grace tenants.
	// enforcer.New is safe with a nil valkey (caching is simply
	// disabled) and only does work when cfg.Billing.Enabled is true
	// (the middleware short-circuits otherwise).
	enf := enforcer.New(rt.Store, rt.Valkey, rt.Logger.Named("billing-enforcer"))
	// The MCP transport uses the JSON-RPC-shaped lifecycle gate so
	// past-due tenants see an in-band `notifications/billing_warning`
	// rather than an HTTP 402 (which MCP clients can't interpret).
	// The gate is mounted on the POST endpoints only — SSE is
	// ungated by design, see internal/transport/mcprs.go.
	mcpBillingMiddleware := enforcer.RequireBillingActiveMCP(rt.Store, enf, rt.Cfg.Billing, rt.Cfg.Billing.PortalOrigin, rt.Logger.Named("billing-lifecycle"))
	if err := mcpmount.Mount(r, rt, mcpServer, mcpAuth, mcpBillingMiddleware); err != nil {
		return err
	}
	return boot.RunHTTPServer(rt, r)
}
