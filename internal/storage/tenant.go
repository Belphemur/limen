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
// The ID is the int64 PK from the tenants table (never the public ULID).
func WithTenant(ctx context.Context, tenantID int64) context.Context {
	return context.WithValue(ctx, ctxKeyTenant, tenantID)
}

// TenantFromCtx returns the tenant ID bound to ctx, or 0/false if none.
func TenantFromCtx(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(ctxKeyTenant).(int64)
	return v, ok && v != 0
}

// WithSuperuser marks ctx as eligible for unscoped access. Session(ctx)
// honors this marker by skipping the tenant SET LOCAL and routing to the
// admin (limen_admin / BYPASSRLS) pool. Reserved for cross-tenant refreshers
// and admin tooling — every call site must be justified in code review.
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

// Session opens a transaction on the appropriate pool, pins the current
// tenant via SET LOCAL when applicable, and returns a *gorm.DB plus a commit
// function.
//
// Routing:
//   - WithSuperuser(ctx) → admin pool, no tenant pin (RLS bypassed by the
//     BYPASSRLS attribute on limen_admin).
//   - WithTenant(ctx, id) → app pool, tenant GUC set; RLS policies enforce
//     isolation.
//   - Neither → ErrNoTenant. Defensive: no unscoped queries on the app pool.
func (s *Store) Session(ctx context.Context) (*gorm.DB, CommitFunc, error) {
	if IsSuperuser(ctx) {
		tx := s.adminDB.WithContext(ctx).Begin()
		if tx.Error != nil {
			return nil, nil, fmt.Errorf("storage: begin admin tx: %w", tx.Error)
		}
		return tx, makeCommit(tx), nil
	}

	tenantID, ok := TenantFromCtx(ctx)
	if !ok {
		return nil, nil, ErrNoTenant
	}
	tx := s.appDB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, nil, fmt.Errorf("storage: begin app tx: %w", tx.Error)
	}
	// SET LOCAL does not accept bind parameters; use set_config(name, value, is_local=true)
	// which is the documented parameterized equivalent.
	if err := tx.Exec(`SELECT set_config('app.current_tenant', ?, true)`, fmt.Sprintf("%d", tenantID)).Error; err != nil {
		_ = tx.Rollback().Error
		return nil, nil, fmt.Errorf("storage: set tenant GUC: %w", err)
	}
	return tx, makeCommit(tx), nil
}

func makeCommit(tx *gorm.DB) CommitFunc {
	done := false
	return func() error {
		if done {
			return nil
		}
		done = true
		if tx.Error != nil {
			_ = tx.Rollback().Error
			return tx.Error
		}
		return tx.Commit().Error
	}
}
