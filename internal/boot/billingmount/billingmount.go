// Package billingmount wires the Stripe billing subsystem into
// the portal binary. It creates the Stripe client, webhook handler,
// BillingService, and reconciler, and registers the Connect-RPC handler
// on the portal API mux.
//
// The webhook endpoint is registered on the root chi router at
// /billing/stripe/webhook (no tenant prefix — Stripe calls this directly).
// The BillingService is registered on the tenant-scoped API mux alongside
// PortalService, AdminService, and SessionService.
//
// Imported only by cmd/portal and cmd/limen (union binary). The MCP
// gateway binary (cmd/gateway) must NOT import this package.
package billingmount

import (
	"net/http"
	"time"

	"github.com/belphemur/limen/internal/billing"
	"github.com/belphemur/limen/internal/billing/stripe"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/session"
)

// Dependencies holds the constructed billing components ready for
// mounting and lifecycle management.
type Dependencies struct {
	// WebhookHandler is the HTTP handler for /billing/stripe/webhook.
	// The caller must mount this on the root chi router and call
	// StartDrain() / StopDrain() for lifecycle.
	WebhookHandler *stripe.WebhookHandler

	// Reconciler periodically syncs billing metrics to Stripe.
	// The caller must call Start(ctx) and Stop() for lifecycle.
	Reconciler *billing.Reconciler

	// ConnectPrefix is the URL path prefix for the BillingService
	// Connect-RPC handler. Register on the portal API mux alongside
	// PortalService/SessionService/AdminService.
	ConnectPrefix string

	// ConnectHandler is the http.Handler for the BillingService
	// Connect-RPC handler.
	ConnectHandler http.Handler
}

// Mount creates the billing subsystem. When cfg.Billing.Enabled is false,
// all components are nil and this returns successfully (billing is
// disabled at runtime — no routes mounted, no goroutines started).
//
// resolver is the session.Resolver used to verify the portal cookie
// (typically session.OIDCResolver(oidc)). Required for the BillingService
// interceptor chain.
//
// Returns nil dependencies + nil error when billing is disabled.
func Mount(rt *boot.Runtime, resolver session.Resolver) (*Dependencies, error) {
	if !rt.Cfg.Billing.Enabled {
		return nil, nil
	}

	// Create shared Stripe client with resilience.
	stripeClient := stripe.NewClient(
		rt.Cfg.Billing,
		rt.Cfg.Resilience.Resolve("billing.stripe"),
		rt.Logger.Named("stripe"),
		rt.Valkey,
	)

	// Create webhook handler.
	webhookHandler := stripe.NewWebhookHandler(
		rt.Store,
		rt.Cfg.Billing.Stripe.WebhookSecret,
		rt.Cfg.Billing,
		rt.Logger.Named("stripe-webhook"),
	)

	// Create BillingService Connect-RPC handler.
	billingSvc := stripe.NewService(
		rt.Store,
		stripeClient,
		rt.Cfg.Billing,
		resolver,
		nil, // bearer intercept is handled by the portal mount
		rt.Logger.Named("billing-service"),
	)
	prefix, handler := billingSvc.Handler()

	// Create reconciler.
	reconciler := billing.NewReconciler(
		rt.Store,
		stripeClient,
		rt.Cfg.Billing,
		time.Hour, // 1-hour periodic reconciliation
		rt.Logger.Named("billing-reconciler"),
	)

	return &Dependencies{
		WebhookHandler: webhookHandler,
		Reconciler:     reconciler,
		ConnectPrefix:  prefix,
		ConnectHandler: handler,
	}, nil
}
