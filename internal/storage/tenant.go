package storage

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ctxKey is the unexported context-key type used for tenant scoping.
type ctxKey int

const (
	ctxKeyTenant ctxKey = iota + 1
	ctxKeySuperuser
)

// ErrNoTenant is returned by Session(ctx) when no tenant is bound to ctx and
// the caller has not opted into the superuser escape hatch.
var ErrNoTenant = errors.New("storage: no tenant in context")

// WithTenant returns a context with the given internal tenant ID bound to it.
// The ID is the int64 PK from the tenants table (never the public KSUID).
func WithTenant(ctx context.Context, tenantID int64) context.Context {
	return context.WithValue(ctx, ctxKeyTenant, tenantID)
}

// TenantFromCtx returns the tenant ID bound to ctx, or 0/false if none.
func TenantFromCtx(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(ctxKeyTenant).(int64)
	return v, ok && v != 0
}

// WithSuperuser marks ctx as eligible for unscoped access. Session(ctx) will
// honor this marker by skipping the tenant SET LOCAL and (Phase 3) routing to
// the limen_admin pool. Reserved for cross-tenant refreshers and admin tooling.
func WithSuperuser(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeySuperuser, true)
}

// IsSuperuser reports whether ctx carries the superuser marker.
func IsSuperuser(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeySuperuser).(bool)
	return v
}

// CommitFunc finalizes the transaction opened by Session. It commits if no
// error has been observed on the returned *gorm.DB and rolls back otherwise.
// Calling it more than once is a no-op.
type CommitFunc func() error

// Session opens a transaction, pins the current tenant via SET LOCAL, and
// returns a tenant-scoped *gorm.DB plus a commit function.
//
// Until Phase 3 ships RLS policies, the GUC is informational — but every call
// site is already wired so the policy rollout is a zero-touch operation.
func (s *Store) Session(ctx context.Context) (*gorm.DB, CommitFunc, error) {
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, nil, fmt.Errorf("storage: begin tx: %w", tx.Error)
	}

	done := false
	commit := func() error {
		if done {
			return nil
		}
		done = true
		if tx.Error != nil {
			tx.Rollback()
			return tx.Error
		}
		return tx.Commit().Error
	}

	if IsSuperuser(ctx) {
		// Skip the tenant pin entirely; Phase 3 will additionally route
		// superuser sessions through the limen_admin pool.
		return tx, commit, nil
	}

	tenantID, ok := TenantFromCtx(ctx)
	if !ok {
		tx.Rollback()
		return nil, nil, ErrNoTenant
	}
	// SET LOCAL does not accept bind parameters; use set_config(name, value, is_local=true)
	// which is the documented parameterized equivalent.
	if err := tx.Exec(`SELECT set_config('app.current_tenant', ?, true)`, fmt.Sprintf("%d", tenantID)).Error; err != nil {
		tx.Rollback()
		return nil, nil, fmt.Errorf("storage: set tenant GUC: %w", err)
	}
	return tx, commit, nil
}
