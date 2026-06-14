package enforcer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/billing/entitlements"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/valkey"
)

const (
	cacheKeyPrefix = "limen:billing:entitlements:"
	defaultTTL     = 5 * time.Minute
)

// sessionStore is the narrow surface entitlementCache needs from storage.
type sessionStore interface {
	Session(ctx context.Context) (*gorm.DB, storage.CommitFunc, error)
}

// entitlementCache provides Valkey-backed caching with DB fallback.
type entitlementCache struct {
	client         valkey.Client
	store          sessionStore
	ttl            time.Duration
	loadFromDBFunc func(ctx context.Context, tenantID int64) (entitlements.PlanEntitlements, error)
}

// newEntitlementCache creates a cache. Pass nil client to skip caching.
func newEntitlementCache(client valkey.Client, store *storage.Store) *entitlementCache {
	return &entitlementCache{client: client, store: store, ttl: defaultTTL}
}

func cacheKey(tenantID int64) string {
	return fmt.Sprintf("%s%d", cacheKeyPrefix, tenantID)
}

// get returns cached entitlements, or nil,nil on miss.
func (c *entitlementCache) get(ctx context.Context, tenantID int64) (*entitlements.PlanEntitlements, error) {
	if c.client == nil {
		return nil, nil
	}
	raw, err := c.client.Get(ctx, cacheKey(tenantID))
	if err != nil {
		return nil, nil // miss
	}
	var e entitlements.PlanEntitlements
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, nil // corrupt entry
	}
	return &e, nil
}

// set stores entitlements in the cache.
func (c *entitlementCache) set(ctx context.Context, tenantID int64, e entitlements.PlanEntitlements) error {
	if c.client == nil {
		return nil
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return c.client.SetEX(ctx, cacheKey(tenantID), raw, c.ttl)
}

// invalidate removes the cache entry for a tenant.
func (c *entitlementCache) invalidate(ctx context.Context, tenantID int64) error {
	if c.client == nil {
		return nil
	}
	return c.client.Del(ctx, cacheKey(tenantID))
}

// loadFromDB loads entitlements from the database.
func (c *entitlementCache) loadFromDB(ctx context.Context, tenantID int64) (entitlements.PlanEntitlements, error) {
	if c.loadFromDBFunc != nil {
		return c.loadFromDBFunc(ctx, tenantID)
	}

	db, commit, err := c.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return entitlements.DeveloperEntitlements(), fmt.Errorf("enforcer: open session: %w", err)
	}
	defer func() { _ = commit() }()

	var rows []storage.TenantEntitlement
	if err := db.Where("tenant_id = ?", tenantID).Find(&rows).Error; err != nil {
		return entitlements.DeveloperEntitlements(), fmt.Errorf("enforcer: query entitlements: %w", err)
	}
	return entitlements.EntitlementsFromRows(rows), nil
}
