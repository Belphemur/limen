package enforcer

import (
	"context"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/tenancy"
)

// BillingInterceptor loads entitlements for the current tenant and
// injects them into the request context. It does NOT enforce specific
// features — that is the responsibility of individual RPC handlers.
//
// Must be placed AFTER session.Interceptor in the chain (needs tenancy
// bound to ctx).
func BillingInterceptor(enforcer *Enforcer, logger *zap.Logger) connect.UnaryInterceptorFunc {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			tenant, ok := tenancy.TenantFromContext(ctx)
			if !ok {
				logger.Debug("billing interceptor: no tenant in context, skipping")
				return next(ctx, req)
			}
			ents, err := enforcer.ForTenant(ctx, tenant.ID)
			if err != nil {
				logger.Warn("billing interceptor: failed to load entitlements",
					zap.Int64("tenant_id", tenant.ID), zap.Error(err))
				return next(ctx, req)
			}
			return next(WithEntitlements(ctx, ents), req)
		}
	}
}
