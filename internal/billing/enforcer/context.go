// Package enforcer provides runtime entitlement enforcement for Limen.
// It resolves a tenant's plan entitlements from cache or database and
// exposes feature-gate helpers that RPC handlers call before executing
// plan-gated operations.
package enforcer

import (
	"context"

	"github.com/belphemur/limen/internal/billing/entitlements"
)

type ctxKey int

const ctxKeyEntitlements ctxKey = iota + 1

// WithEntitlements stores entitlements on ctx.
func WithEntitlements(ctx context.Context, e entitlements.PlanEntitlements) context.Context {
	return context.WithValue(ctx, ctxKeyEntitlements, e)
}

// EntitlementsFromContext returns (zero-value, false) if none are stored.
func EntitlementsFromContext(ctx context.Context) (entitlements.PlanEntitlements, bool) {
	e, ok := ctx.Value(ctxKeyEntitlements).(entitlements.PlanEntitlements)
	return e, ok
}
