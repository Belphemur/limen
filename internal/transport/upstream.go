// Package transport — upstream callback wiring.
//
// Phase 7 only owns one HTTP route: the OAuth redirect URI that upstream
// Authorization Servers redirect the user to after they authorize. The
// route lives under /t/{tenant}/upstream/{name}/callback behind the
// usual tenancy.RequireTenant + OIDC.RequireSession middlewares.
//
// Every other portal action (StartConnect, Disconnect, SubmitUpstreamAPIKey)
// is a plain Go method on upstream.Service. Phase 9 wraps them via
// Connect-RPC; do not add HTTP routes for them here.
package transport

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
)

// UpstreamDeps wires the upstream callback handler.
type UpstreamDeps struct {
	Store   *storage.Store
	OIDC    *auth.OIDC
	Service *upstream.Service
	Logger  *zap.Logger
}

// MountUpstream attaches the /callback route to r.
func MountUpstream(r chi.Router, deps UpstreamDeps) {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	r.Route("/t/{tenant}/upstream/{name}/callback", func(cr chi.Router) {
		cr.Use(tenancy.RequireTenant(deps.Store, logger))
		cr.Use(deps.OIDC.RequireSession())
		cr.Get("/", upstreamCallbackHandler(deps, logger))
	})
}

func upstreamCallbackHandler(deps UpstreamDeps, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tenant, ok := tenancy.TenantFromContext(ctx)
		if !ok {
			http.Error(w, "tenant required", http.StatusBadRequest)
			return
		}
		claims, ok := auth.ClaimsFromContext(ctx)
		if !ok {
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		upstreamName := chi.URLParam(r, "name")

		user, err := loadUserBySubject(ctx, deps.Store, tenant.ID, claims.GetSubject())
		if err != nil {
			logger.Warn("upstream callback: user lookup failed",
				zap.String("tenant", tenant.PublicID),
				zap.String("upstream", upstreamName),
				zap.Error(err))
			http.Error(w, "user not found", http.StatusForbidden)
			return
		}

		returnTo, err := deps.Service.FinishCallback(ctx, tenant, user, upstreamName, r.URL.RawQuery)
		if err != nil {
			logger.Warn("upstream callback: finish failed",
				zap.String("tenant", tenant.PublicID),
				zap.String("upstream", upstreamName),
				zap.Error(err))
			http.Error(w, "callback failed", http.StatusBadRequest)
			return
		}

		// Phase 8 \u2014 the first tenant owner/admin to complete the link
		// bootstraps the shared upstream tool catalog. Member links never
		// refresh the catalog; the role check is the canonical gate.
		// Best-effort: indexing failure logs but does not fail the
		// redirect back to the SPA \u2014 the periodic sweep will retry.
		if hasCatalogIndexerRole(claims) {
			up, lerr := deps.Service.LoadUpstream(ctx, tenant.ID, upstreamName)
			if lerr != nil {
				logger.Warn("upstream callback: load upstream for catalog index failed",
					zap.String("tenant", tenant.PublicID),
					zap.String("upstream", upstreamName),
					zap.Error(lerr))
			} else {
				link, lerr := deps.Service.LoadLink(ctx, tenant.ID, user.ID, up.ID)
				switch {
				case lerr != nil && !errors.Is(lerr, upstream.ErrLinkNotFound):
					logger.Warn("upstream callback: load link for catalog index failed",
						zap.String("tenant", tenant.PublicID),
						zap.String("upstream", upstreamName),
						zap.Error(lerr))
				case link == nil:
					logger.Debug("upstream callback: no link yet, skipping catalog index",
						zap.String("tenant", tenant.PublicID),
						zap.String("upstream", upstreamName))
				default:
					if ierr := deps.Service.IndexCatalog(ctx, tenant, up, link); ierr != nil {
						logger.Warn("upstream callback: catalog index failed",
							zap.String("tenant", tenant.PublicID),
							zap.String("upstream", upstreamName),
							zap.Error(ierr))
					} else {
						logger.Info("upstream callback: catalog indexed",
							zap.String("tenant", tenant.PublicID),
							zap.String("upstream", upstreamName))
					}
				}
			}
		}

		if returnTo == "" {
			returnTo = "/t/" + tenant.PublicID + "/portal"
		}
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
	}
}

// hasCatalogIndexerRole reports whether the caller's verified ID-token
// claims include a role that authorizes them to bootstrap or refresh the
// shared upstream tool catalog. Members are deliberately excluded \u2014 the
// resulting catalog is shared with every user in the tenant, so the
// bootstrap is an admin responsibility.
func hasCatalogIndexerRole(claims *oidc.IDTokenClaims) bool {
	for _, role := range auth.ExtractRoles(claims) {
		switch role {
		case "owner", "admin":
			return true
		}
	}
	return false
}

// loadUserBySubject reads the local User row by (tenant_id, zitadel_subject).
// Runs on the tenant-scoped pool; RLS guards us if the middleware chain
// is misconfigured.
func loadUserBySubject(ctx context.Context, store *storage.Store, tenantID int64, sub string) (*storage.User, error) {
	if sub == "" {
		return nil, errors.New("upstream callback: empty zitadel subject")
	}
	tx, commit, err := store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var u storage.User
	if err := tx.Where("tenant_id = ? AND zitadel_subject = ?", tenantID, sub).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("upstream callback: user not found")
		}
		return nil, err
	}
	return &u, nil
}
