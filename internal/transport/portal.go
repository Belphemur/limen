// Package transport's portal.go wires the Phase 4 OIDC routes onto a chi
// router.
//
// Layout:
//
//	  GET  /auth/callback                          (root — tenant public id rides in signed state)
//	  GET  /auth/discovery                         (root — exposes the configured Zitadel issuer URL)
//	  GET  /portal[/]                              (root — sends the browser to login, returns to /t/{tenant}/portal/)
//	  GET  /admin[/]                               (root — sends the browser to login, returns to /t/{tenant}/admin/)
//	/api/limen.signup.v1.SignupService/*         (root — tenant-agnostic SignupService)
//	/t/{tenant}                                  (subrouter behind tenancy.RequireTenant; {tenant} is a tnt_<ULID> public id)
//	  GET  /auth/login
//	  GET  /auth/logout
//	  /api/*                                     (Connect-RPC portal / session / admin services)
//
// Tenant resolution + User upsert are wired here so internal/auth has no
// dependency on internal/storage.
package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

// PortalDeps bundles everything MountPortal needs.
type PortalDeps struct {
	Store                 *storage.Store
	OIDC                  *auth.OIDC
	Logger                *zap.Logger
	PostLogoutRedirectURI string
	// OIDCIssuer is the configured Zitadel issuer URL surfaced by
	// GET /auth/discovery. Empty disables the discovery endpoint.
	OIDCIssuer string
	// ConnectAPI, when non-nil, is mounted at /t/{tenant}/api/* — used
	// to wire per-tenant Connect-RPC handlers (PortalService +
	// SessionService + AdminService). The handler is expected to
	// already carry its own interceptor stack (tenancy / session /
	// role) and to multiplex the per-service procedure prefixes via an
	// http.ServeMux.
	ConnectAPI http.Handler
	// SignupAPI, when non-nil, is mounted at /api/* on the root router
	// for the tenant-agnostic SignupService. No tenancy middleware
	// fires here.
	SignupAPI http.Handler
}

// MountPortal attaches the OIDC + tenant routes onto r.
func MountPortal(r chi.Router, deps PortalDeps) {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	resolveTenant := func(ctx context.Context, tenantPublicID, orgID string) (int64, string, string, error) {
		var t *storage.Tenant
		var err error
		if tenantPublicID != "" {
			t, err = tenancy.Resolve(ctx, deps.Store, tenantPublicID)
		} else {
			t, err = tenancy.ResolveByZitadelOrg(ctx, deps.Store, orgID)
		}
		if err != nil {
			return 0, "", "", err
		}
		return t.ID, t.PublicID, t.ZitadelOrgID, nil
	}
	upsertUser := func(ctx context.Context, tenantID int64, sub, email, name string) error {
		return upsertPortalUser(ctx, deps.Store, tenantID, sub, email, name)
	}

	r.Get("/auth/callback", deps.OIDC.CallbackHandler(resolveTenant, upsertUser))
	// Tenant-agnostic login entry point: lets a user click "Sign in" on
	// the root SPA shell without knowing their tenant slug. The
	// callback resolves the tenant from the token's home-org claim.
	r.Get("/auth/login", deps.OIDC.LoginHandler())
	// Root-level SPA entry points. Hitting /portal or /admin without
	// knowing your tenant slug funnels through /auth/login; the
	// callback resolves the tenant from the Zitadel home-org claim and
	// lands the browser at /t/{publicID}/portal/ (or /admin/). When an
	// SSO session is already active at Zitadel the round trip is
	// invisible to the user — prompt=none semantics.
	portalEntry := rootAppRedirect("/portal/")
	adminEntry := rootAppRedirect("/admin/")
	r.Get("/portal", portalEntry)
	r.Get("/portal/", portalEntry)
	r.Get("/admin", adminEntry)
	r.Get("/admin/", adminEntry)
	if deps.OIDCIssuer != "" {
		r.Get("/auth/discovery", DiscoveryHandler(deps.OIDCIssuer))
	}

	if deps.SignupAPI != nil {
		r.Mount("/api", connectMountAdapter(deps.SignupAPI))
	}

	r.Route("/t/{tenant}", func(tr chi.Router) {
		tr.Use(tenancy.RequireTenant(deps.Store, logger))
		tr.Get("/auth/login", deps.OIDC.LoginHandler())
		tr.Get("/auth/logout", deps.OIDC.LogoutHandler(deps.PostLogoutRedirectURI))

		if deps.ConnectAPI != nil {
			// chi.Mount tracks the path remainder in
			// chi.RouteContext().RoutePath but does not modify r.URL.Path,
			// so Connect (which dispatches on r.URL.Path) would see the
			// full "/t/{tenant}/api/limen.portal.v1.PortalService/..."
			// and 404. Rewrite r.URL.Path to the unmatched remainder so
			// the Connect handler sees its own procedure prefix.
			tr.Mount("/api", connectMountAdapter(deps.ConnectAPI))
		}
	})
}

// rootAppRedirect bounces the browser through /auth/login with the
// given return_to so the OIDC callback lands them at
// /t/{publicID}{returnTo} after the tenant is resolved from the user's
// Zitadel home-org claim.
func rootAppRedirect(returnTo string) http.HandlerFunc {
	target := "/auth/login?return_to=" + url.QueryEscape(returnTo)
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// connectMountAdapter rewrites r.URL.Path to the chi-tracked path
// remainder before delegating, so a Connect handler mounted at
// /t/{tenant}/api/* sees procedure paths like
// "/limen.portal.v1.PortalService/GetSession" — what connect-go's
// router dispatches on.
func connectMountAdapter(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.RouteContext(r.Context())
		if rctx == nil || rctx.RoutePath == "" {
			h.ServeHTTP(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = rctx.RoutePath
		r2.URL.RawPath = ""
		h.ServeHTTP(w, r2)
	})
}

// upsertPortalUser writes the local User row keyed by (tenant_id, zitadel_subject).
// Runs on the admin pool so it works regardless of the request's tenant
// pin; the unique index on (tenant_id, zitadel_subject) is the source of
// truth for idempotency.
func upsertPortalUser(ctx context.Context, store *storage.Store, tenantID int64, sub, email, name string) error {
	if sub == "" {
		return errors.New("transport: missing zitadel subject on callback")
	}
	if email == "" {
		return errors.New("transport: missing email on callback")
	}
	if name == "" {
		name = email
	}

	db, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return fmt.Errorf("transport: open session: %w", err)
	}
	defer func() { _ = commit() }()

	var existing storage.User
	err = db.Where("tenant_id = ? AND zitadel_subject = ?", tenantID, sub).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		u := &storage.User{
			TenantID:       tenantID,
			Email:          email,
			Name:           name,
			ZitadelSubject: sub,
		}
		if err := db.Create(u).Error; err != nil {
			return fmt.Errorf("transport: create user: %w", err)
		}
	case err != nil:
		return fmt.Errorf("transport: load user: %w", err)
	default:
		dirty := false
		if existing.Email != email {
			existing.Email = email
			dirty = true
		}
		if existing.Name != name {
			existing.Name = name
			dirty = true
		}
		if dirty {
			if err := db.Save(&existing).Error; err != nil {
				return fmt.Errorf("transport: update user: %w", err)
			}
		}
	}
	return commit()
}
