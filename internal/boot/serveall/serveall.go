// Package serveall is the all-in-one binary entry point used by
// cmd/limen (and by 'limen serve' under cmd/limenctl's cobra root).
// Mounts the UNION of every route the split binaries collectively
// expose — adding a route to one of the split binaries without folding
// it in here is a regression.
package serveall

import (
	"context"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/billing/metrics"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/boot/billingmount"
	"github.com/belphemur/limen/internal/boot/mcpmount"
	"github.com/belphemur/limen/internal/boot/oauthproxymount"
	"github.com/belphemur/limen/internal/boot/oidcboot"
	"github.com/belphemur/limen/internal/boot/portalmount"
	"github.com/belphemur/limen/internal/boot/upstreammount"
	"github.com/belphemur/limen/internal/boot/zitadelboot"
	"github.com/belphemur/limen/internal/mcprs"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/signup"
)

// Run boots the all-in-one runtime and serves until SIGINT/SIGTERM.
func Run(configPath string) error {
	rt, cleanup, err := boot.BootRuntime(configPath, boot.AllProfiles)
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

	mgr, mcpServer, err := mcpmount.Build(rt)
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
	r.Get("/", boot.LandingPage)
	boot.MountHealth(r)

	resolver := session.OIDCResolver(oidc)

	api, signupSvc, err := portalmount.Mount(r, rt, oidc, bearerIntercept, zclient, zclient, zclient, zclient, resolver)
	if err != nil {
		return err
	}

	billingDeps, err := billingmount.Mount(rt, resolver)
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

		// Wire billing recorder → reconciler for reactive reconciliation.
		if mgr.BillingRecorder() != nil && billingDeps != nil {
			mgr.BillingRecorder().SetReactiveTrigger(billingDeps.Reconciler.ReactiveTrigger)
		}
	}

	if rt.Cfg.Signup.Enabled && signupSvc != nil {
		go signup.NewSweeper(rt.Store, rt.Logger.Named("signup-sweeper")).Run(rt.Ctx)
	}
	if err := oauthproxymount.Mount(r, rt, zclient); err != nil {
		return err
	}
	if err := mcpmount.Mount(r, rt, mcpServer, mcpAuth); err != nil {
		return err
	}
	upstreammount.Mount(r, rt, oidc)

	// Start billing metrics consumer if Valkey is enabled
	if rt.Valkey != nil {
		consumer := metrics.NewConsumer(rt.Valkey, rt.Store, rt.Logger.Named("billing-consumer"), "limen-allinone")
		go consumer.Run(rt.Ctx)
	}

	return boot.RunHTTPServer(rt, r)
}
