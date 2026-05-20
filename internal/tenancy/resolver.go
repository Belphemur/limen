// Package tenancy resolves the URL-path tenant public id into a *Tenant
// and binds it to the request context for downstream middlewares.
//
// The tenant table is *not* RLS-scoped (see internal/storage/migrations
// /postgres/00001_rls.sql — `tenants` is intentionally absent from the
// FORCE-RLS list), so the resolver runs against the admin pool via
// storage.WithSuperuser. Once resolved, the request context carries
// `storage.WithTenant(ctx, tenant.ID)` for every later DB hit on the
// request path.
package tenancy

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/ids"
	"github.com/belphemur/limen/internal/storage"
)

// ErrNotFound is returned by Resolve when no tenant exists for the public id.
var ErrNotFound = errors.New("tenancy: tenant not found")

type ctxKey int

const ctxKeyTenant ctxKey = iota + 1

// WithTenant stores the resolved tenant on ctx. Mirrors the storage-side
// WithTenant pin so middlewares downstream of `RequireTenant` can access
// the whole row without re-querying.
func WithTenant(ctx context.Context, t *storage.Tenant) context.Context {
	return context.WithValue(ctx, ctxKeyTenant, t)
}

// TenantFromContext returns the tenant bound to ctx by `RequireTenant`.
func TenantFromContext(ctx context.Context) (*storage.Tenant, bool) {
	t, ok := ctx.Value(ctxKeyTenant).(*storage.Tenant)
	return t, ok && t != nil
}

// MustTenant returns the tenant or panics. Use only inside handlers
// already protected by `RequireTenant`.
func MustTenant(ctx context.Context) *storage.Tenant {
	t, ok := TenantFromContext(ctx)
	if !ok {
		panic("tenancy: tenant not in context — missing RequireTenant middleware")
	}
	return t
}

// Resolve looks up a tenant by its public id (e.g. "tnt_01H..."). Runs
// against the admin pool (the tenants table is not RLS-scoped). The public
// id is structurally validated before hitting the database to keep
// malformed inputs from generating noise.
func Resolve(ctx context.Context, store *storage.Store, publicID string) (*storage.Tenant, error) {
	if _, err := ids.MustParse(ids.PrefixTenant, publicID); err != nil {
		return nil, ErrNotFound
	}
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return nil, err
	}
	var t storage.Tenant
	err = tx.Where("public_id = ?", publicID).First(&t).Error
	commitErr := commit()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if commitErr != nil {
		return nil, commitErr
	}
	return &t, nil
}

// ResolveByZitadelOrg looks up the tenant bound to a Zitadel org id. Used
// by the tenant-agnostic /auth/login flow so the callback handler can
// pick the right tenant from the user's home-org claim.
func ResolveByZitadelOrg(ctx context.Context, store *storage.Store, orgID string) (*storage.Tenant, error) {
	if orgID == "" {
		return nil, ErrNotFound
	}
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return nil, err
	}
	var t storage.Tenant
	err = tx.Where("zitadel_org_id = ?", orgID).First(&t).Error
	commitErr := commit()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if commitErr != nil {
		return nil, commitErr
	}
	return &t, nil
}

// RequireTenant is a chi middleware mounted on the /t/{tenant}/* subrouter.
// It reads the {tenant} URL param (the tenant's public id), looks up the
// row, and pushes both the tenant pointer and the storage-level tenant pin
// onto the request context.
//
// Unknown / malformed id → 404 with a generic message (no enumeration).
func RequireTenant(store *storage.Store, logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			publicID := chi.URLParam(r, "tenant")
			if publicID == "" {
				http.NotFound(w, r)
				return
			}
			t, err := Resolve(r.Context(), store, publicID)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					http.NotFound(w, r)
					return
				}
				logger.Error("tenant resolution failed", zap.String("tenant", publicID), zap.Error(err))
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			ctx := WithTenant(r.Context(), t)
			ctx = storage.WithTenant(ctx, t.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
