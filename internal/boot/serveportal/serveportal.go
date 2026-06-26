// Package serveportal is the portal binary entry point used by
// cmd/portal. It mounts the customer / admin Connect-RPC routes
// (Phase 9b/9c land the actual services), the OIDC RP (/auth/*), the
// OAuth proxy (DCR + AS metadata + redirector), and the upstream OAuth
// callback. Holds the most privileged secrets in the system (the
// Zitadel management credential and the portal-session cipher key).
package serveportal

import (
	"context"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/billing/enforcer"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/boot/billingmount"
	"github.com/belphemur/limen/internal/boot/oauthproxymount"
	"github.com/belphemur/limen/internal/boot/oidcboot"
	"github.com/belphemur/limen/internal/boot/portalmount"
	"github.com/belphemur/limen/internal/boot/upstreammount"
	"github.com/belphemur/limen/internal/boot/zitadelboot"
	"github.com/belphemur/limen/internal/mcprs"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/signup"
)

// Run boots a portal runtime and serves until SIGINT/SIGTERM.
func Run(configPath string) error {
	profile := boot.NeedStore | boot.NeedCipher | boot.NeedSigner | boot.NeedUpstream
	rt, cleanup, err := boot.BootRuntime(configPath, profile)
	if err != nil {
		return err
	}
	defer cleanup()

	oidc, err := oidcboot.Build(rt)
	if err != nil {
		return err
	}
	zclient, err := zitadelboot.Build(rt)
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

	bearerIntercept := session.BearerTokenInterceptor(
		session.BearerTokenConfig{Verifier: mcpAuth.Verifier(), Audience: rt.Cfg.Zitadel.MCPResourceAudience},
		rt.Store,
		rt.Logger,
	)

	r := chi.NewRouter()
	r.Use(boot.PermissiveCORS)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(boot.RequestLogger(rt.Logger))
	boot.MountHealth(r)

	resolver := session.OIDCResolver(oidc)

	billingDeps, err := billingmount.Mount(rt, resolver)
	if err != nil {
		return err
	}

	var enf *enforcer.Enforcer
	if billingDeps != nil {
		enf = billingDeps.Enforcer
	}

	var billingIntercept connect.UnaryInterceptorFunc
	if enf != nil {
		billingIntercept = enforcer.BillingInterceptor(enf, rt.Logger.Named("billing-interceptor"))
	}

	// Pass the enforcer through so the lifecycle middleware can
	// invalidate the entitlement cache after a one-time auto-downgrade
	// for cancelled/expired-grace tenants. enf is nil when billing is
	// disabled — the middleware short-circuits before touching it.
	//
	// The portal binary doesn't mount MCP routes (those live on the
	// gateway / all-in-one binaries), so only the Connect-RPC-style
	// HTTP 402 gate is needed here. The MCP transport uses
	// RequireBillingActiveMCP separately in servegateway / serveall.
	billingMiddleware := enforcer.RequireBillingActive(rt.Store, enf, rt.Cfg.Billing, rt.Logger.Named("billing-lifecycle"))

	api, signupSvc, err := portalmount.Mount(r, rt, oidc, bearerIntercept, billingIntercept, zclient, zclient, zclient, zclient, resolver, billingMiddleware)
	if err != nil {
		return err
	}

	if billingDeps != nil {
		api.Handle(billingDeps.ConnectPrefix, billingDeps.ConnectHandler)
		r.Handle("/billing/stripe/webhook", billingDeps.WebhookHandler)
		billingDeps.WebhookHandler.StartDrain()
		rt.AddCleanup(func() { billingDeps.WebhookHandler.StopDrain() })
		go billingDeps.Reconciler.Start(rt.Ctx)
		rt.AddCleanup(func() { billingDeps.Reconciler.Stop() })

		// Run startup reconciliation in background to repair any missed
		// webhook deliveries during portal downtime.
		go func() {
			count, err := billingDeps.Reconciler.ReconcileNow(context.Background())
			if err != nil {
				rt.Logger.Error("startup billing reconciliation failed", zap.Error(err))
			} else {
				rt.Logger.Info("startup billing reconciliation complete", zap.Int("tenants_reconciled", count))
			}
		}()
	}

	if rt.Cfg.Signup.Enabled && signupSvc != nil {
		go signup.NewSweeper(rt.Store, rt.Logger.Named("signup-sweeper")).Run(rt.Ctx)
	}
	if err := oauthproxymount.Mount(r, rt, zclient); err != nil {
		return err
	}
	upstreammount.Mount(r, rt, oidc)

	return boot.RunHTTPServer(rt, r)
}
