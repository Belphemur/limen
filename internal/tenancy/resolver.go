// Package tenancy resolves the URL-path tenant slug into a *Tenant and
// binds it to the request context for downstream middlewares.
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
	"regexp"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/storage"
)

// SlugPattern is the canonical tenant slug regex: lowercase alphanumerics
// plus internal hyphens, 1–32 chars, no leading/trailing hyphen.
var SlugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

// reservedSlugs collides with platform paths or has special meaning. The
// list is checked at tenant *creation* time, not at resolve time, so a row
// that exists is by construction safe.
//
// The operator backoffice slug "_staff" is intentionally not in this list:
// it is structurally outside the customer regex (the leading underscore
// fails [a-z0-9]) so it cannot collide with a customer slug. Phase 12
// provisions it directly through limen-migrate.
var reservedSlugs = map[string]struct{}{
	"api":         {},
	"oauth":       {},
	"oidc":        {},
	"portal":      {},
	"t":           {},
	"admin":       {},
	"static":      {},
	"mcp":         {},
	".well-known": {},
	"public":      {},
	"health":      {},
	"metrics":     {},
	"login":       {},
	"logout":      {},
	"register":    {},
	"robots.txt":  {},
	"favicon.ico": {},
	"auth":        {},
}

// ErrInvalidSlug is returned by ValidateSlug for a syntactically invalid slug.
var ErrInvalidSlug = errors.New("tenancy: invalid slug")

// ErrReservedSlug is returned by ValidateSlug for a slug on the reserved list.
var ErrReservedSlug = errors.New("tenancy: reserved slug")

// ErrNotFound is returned by Resolve when no tenant exists for the slug.
var ErrNotFound = errors.New("tenancy: tenant not found")

// ValidateSlug checks both the regex and the reserved-name list. Use this
// at tenant *creation* sites; runtime resolution does not need to re-check.
func ValidateSlug(slug string) error {
	if !SlugPattern.MatchString(slug) {
		return ErrInvalidSlug
	}
	if _, ok := reservedSlugs[slug]; ok {
		return ErrReservedSlug
	}
	return nil
}

// IsReserved reports whether the given (already syntactically valid) slug
// is on the reserved list. Exported for tests and admin tooling.
func IsReserved(slug string) bool {
	_, ok := reservedSlugs[slug]
	return ok
}

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

// Resolve looks up a tenant by slug. Runs against the admin pool (the
// tenants table is not RLS-scoped).
func Resolve(ctx context.Context, store *storage.Store, slug string) (*storage.Tenant, error) {
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return nil, err
	}
	var t storage.Tenant
	err = tx.Where("slug = ?", slug).First(&t).Error
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
// It reads the {tenant} URL param, looks up the row, and pushes both the
// tenant pointer and the storage-level tenant pin onto the request context.
//
// Unknown slug → 404 with a generic message (no enumeration).
func RequireTenant(store *storage.Store, logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slug := chi.URLParam(r, "tenant")
			if slug == "" {
				http.NotFound(w, r)
				return
			}
			t, err := Resolve(r.Context(), store, slug)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					http.NotFound(w, r)
					return
				}
				logger.Error("tenant resolution failed", zap.String("slug", slug), zap.Error(err))
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			ctx := WithTenant(r.Context(), t)
			ctx = storage.WithTenant(ctx, t.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
