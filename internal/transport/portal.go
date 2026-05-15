// Package transport's portal.go wires the Phase 4 OIDC routes onto a chi
// router.
//
// Layout:
//
//	GET  /auth/callback                          (root — tenant public id rides in signed state)
//	/t/{tenant}                                  (subrouter behind tenancy.RequireTenant; {tenant} is a tnt_<ULID> public id)
//	  GET  /auth/login
//	  GET  /auth/logout
//
// Tenant resolution + User upsert are wired here so internal/auth has no
// dependency on internal/storage.
package transport

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
)

// PortalDeps bundles everything MountPortal needs.
type PortalDeps struct {
	Store                 *storage.Store
	OIDC                  *auth.OIDC
	Logger                *zap.Logger
	PostLogoutRedirectURI string
	// UpstreamService, when non-nil, enables the portal PoC endpoints
	// for connecting/disconnecting MCP upstreams. Phase 9 replaces these
	// with a Connect-RPC service.
	UpstreamService *upstream.Service
}

// MountPortal attaches the OIDC + tenant routes onto r.
func MountPortal(r chi.Router, deps PortalDeps) {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	resolveTenant := func(ctx context.Context, tenantPublicID string) (int64, string, error) {
		t, err := tenancy.Resolve(ctx, deps.Store, tenantPublicID)
		if err != nil {
			return 0, "", err
		}
		return t.ID, t.ZitadelOrgID, nil
	}
	upsertUser := func(ctx context.Context, tenantID int64, sub, email, name string) error {
		return upsertPortalUser(ctx, deps.Store, tenantID, sub, email, name)
	}

	r.Get("/auth/callback", deps.OIDC.CallbackHandler(resolveTenant, upsertUser))

	r.Route("/t/{tenant}", func(tr chi.Router) {
		tr.Use(tenancy.RequireTenant(deps.Store, logger))
		tr.Get("/auth/login", deps.OIDC.LoginHandler())
		tr.Get("/auth/logout", deps.OIDC.LogoutHandler(deps.PostLogoutRedirectURI))

		tr.Route("/portal", func(pr chi.Router) {
			pr.Use(deps.OIDC.RequireSession())
			pr.Get("/me", portalMeHandler)
			mountPortalUpstreams(pr, deps)
			pr.Get("/", portalStaticHandler())
			pr.Get("/*", portalStaticHandler())
		})
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
