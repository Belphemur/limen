package enforcer

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/billing/entitlements"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/valkey"
)

// Enforcer resolves entitlements for a tenant and provides cache
// invalidation. Safe for concurrent use. When valkeyClient is nil,
// caching is disabled and every call hits the database.
type Enforcer struct {
	cache  *entitlementCache
	logger *zap.Logger
}

// New creates an Enforcer. Pass nil valkeyClient to skip caching.
func New(store *storage.Store, valkeyClient valkey.Client, logger *zap.Logger) *Enforcer {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Enforcer{
		cache:  newEntitlementCache(valkeyClient, store),
		logger: logger,
	}
}

// ForTenant resolves the entitlements for a tenant. Cache-first with DB
// fallback. Newly loaded entitlements are written back to cache.
func (e *Enforcer) ForTenant(ctx context.Context, tenantID int64) (entitlements.PlanEntitlements, error) {
	// Try cache first.
	cached, err := e.cache.get(ctx, tenantID)
	if err != nil {
		e.logger.Debug("enforcer: cache get error", zap.Int64("tenant_id", tenantID), zap.Error(err))
	}
	if cached != nil {
		return *cached, nil
	}

	// Load from DB.
	ents, err := e.cache.loadFromDB(ctx, tenantID)
	if err != nil {
		e.logger.Warn("enforcer: db load failed, using developer defaults",
			zap.Int64("tenant_id", tenantID), zap.Error(err))
		return entitlements.DeveloperEntitlements(), nil
	}

	// Cache the result (best-effort).
	if setErr := e.cache.set(ctx, tenantID, ents); setErr != nil {
		e.logger.Debug("enforcer: cache set error", zap.Int64("tenant_id", tenantID), zap.Error(setErr))
	}

	return ents, nil
}

// Invalidate removes cached entitlements for a tenant. Call from webhook
// handler when entitlements change.
func (e *Enforcer) Invalidate(ctx context.Context, tenantID int64) error {
	if err := e.cache.invalidate(ctx, tenantID); err != nil {
		return fmt.Errorf("enforcer: invalidate tenant %d: %w", tenantID, err)
	}
	return nil
}
