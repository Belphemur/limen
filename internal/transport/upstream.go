// Package transport — upstream callback wiring.
//
// Phase 7 only owns one HTTP route: the OAuth redirect URI that upstream
// Authorization Servers redirect the user to after they authorize. The
// route lives under /t/{tenant}<CallbackPath>/{publicId}/callback behind the
// usual tenancy.RequireTenant + OIDC.RequireSession middlewares; the
// path segment is configurable (default "/mcp-servers") via
// server.upstream_callback_path so deployments can rename it without
// touching code.
//
// Every other portal action (StartConnect, Disconnect, SubmitUpstreamAPIKey)
// is a plain Go method on upstream.Service. Phase 9b wraps them via
// Connect-RPC; do not add HTTP routes for them here.
package transport

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/oauthstate"
)

// UpstreamDeps wires the upstream callback handler.
type UpstreamDeps struct {
	Store   *storage.Store
	OIDC    *auth.OIDC
	Service *upstream.Service
	Logger  *zap.Logger
	// CallbackPath is the single path segment between /t/{tenant} and
	// /{publicId}/callback, e.g. "/mcp-servers". Required; must start with
	// "/" and contain no trailing slash.
	CallbackPath string
}

// MountUpstream attaches the /callback route to r.
func MountUpstream(r chi.Router, deps UpstreamDeps) {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	if deps.CallbackPath == "" {
		logger.Error("transport.MountUpstream: CallbackPath is empty; refusing to mount")
		return
	}
	r.Route("/t/{tenant}"+deps.CallbackPath+"/{publicId}/callback", func(cr chi.Router) {
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
		upstreamPublicID := chi.URLParam(r, "publicId")

		user, err := loadUserBySubject(ctx, deps.Store, tenant.ID, claims.GetSubject())
		if err != nil {
			logger.Warn("upstream callback: user lookup failed",
				zap.String("tenant", tenant.PublicID),
				zap.String("upstream", upstreamPublicID),
				zap.Error(err))
			http.Error(w, "user not found", http.StatusForbidden)
			return
		}

		logger.Debug("upstream callback: attempting FinishTenantCallback",
			zap.String("tenant", tenant.PublicID),
			zap.Int64("tenant_id", tenant.ID),
			zap.String("upstream", upstreamPublicID),
			zap.String("user_subject", claims.GetSubject()))

		returnTo, err := deps.Service.FinishTenantCallback(ctx, tenant, user, upstreamPublicID, r.URL.RawQuery)
		if err == nil {
			// Tenant-level OAuth succeeded — no per-user link to verify.
			if returnTo == "" {
				returnTo = "/t/" + tenant.PublicID + "/"
			}
			http.Redirect(w, r, returnTo, http.StatusSeeOther)
			return
		}

		// Only fall back to the per-user flow when the tenant state envelope
		// is missing (wrong kind, expired, or already consumed). Any other
		// error (AS rejection, network failure, etc.) is terminal.
		if !errors.Is(err, oauthstate.ErrNotFound) {
			logger.Warn("upstream callback: tenant finish failed",
				zap.String("tenant", tenant.PublicID),
				zap.String("upstream", upstreamPublicID),
				zap.Error(err))
			if deps.handleCallbackError(w, r, err, "callback failed") {
				return
			}
			return
		}

		logger.Debug("upstream callback: tenant state not found, falling back to per-user FinishCallback",
			zap.String("tenant", tenant.PublicID),
			zap.String("upstream", upstreamPublicID))

		returnTo, err = deps.Service.FinishCallback(ctx, tenant, user, upstreamPublicID, r.URL.RawQuery)
		if err != nil {
			logger.Warn("upstream callback: finish failed",
				zap.String("tenant", tenant.PublicID),
				zap.String("upstream", upstreamPublicID),
				zap.Error(err))
			if deps.handleCallbackError(w, r, err, "callback failed") {
				return
			}
			return
		}

		// Phase 8 — the first tenant owner/admin to complete the link
		// bootstraps the shared upstream tool catalog. Member links never
		// refresh the catalog; the role check is the canonical gate.
		// Best-effort: indexing failure logs but does not fail the
		// redirect back to the SPA — the periodic sweep will retry.
		up, lerr := deps.Service.LoadUpstreamByPublicID(ctx, tenant.ID, upstreamPublicID)
		if lerr != nil {
			logger.Warn("upstream callback: load upstream for verification failed",
				zap.String("tenant", tenant.PublicID),
				zap.String("upstream", upstreamPublicID),
				zap.Error(lerr))
		} else {
			link, llerr := deps.Service.LoadLink(ctx, tenant.ID, user.ID, up.ID)
			switch {
			case llerr != nil && !errors.Is(llerr, upstream.ErrLinkNotFound):
				logger.Warn("upstream callback: load link for verification failed",
					zap.String("tenant", tenant.PublicID),
					zap.String("upstream", upstreamPublicID),
					zap.Error(llerr))
			case link == nil:
				logger.Debug("upstream callback: no link yet, skipping verification",
					zap.String("tenant", tenant.PublicID),
					zap.String("upstream", upstreamPublicID))
			default:
				// Verify the upstream actually accepts the freshly
				// issued credentials. Some authorization servers
				// (PayPal, observed) hand back a token even when the
				// user refused consent — the AS round-trip looks
				// successful but the resource server then rejects
				// every call (401, or 404 when the MCP endpoint is
				// scope-gated). Any verification failure here means
				// the link does not currently work, so we undo it
				// rather than persist a green-checked but broken
				// upstream. The admin can retry the consent flow.
				if verr := deps.Service.VerifyLink(ctx, tenant, up, link); verr != nil {
					logger.Warn("upstream callback: link verification failed",
						zap.String("tenant", tenant.PublicID),
						zap.String("upstream", upstreamPublicID),
						zap.Error(verr))
					if derr := deps.Service.Disconnect(ctx, tenant, user, upstreamPublicID); derr != nil {
						logger.Warn("upstream callback: rollback after verification failure",
							zap.String("tenant", tenant.PublicID),
							zap.String("upstream", upstreamPublicID),
							zap.Error(derr))
					}
					if returnTo == "" {
						http.Error(w, "upstream rejected the issued credentials", http.StatusBadGateway)
						return
					}
					redirectWithOAuthError(w, r, returnTo, "access_denied",
						"The MCP server rejected the issued credentials. The consent flow may have been refused or the granted scopes are insufficient.")
					return
				}
				if hasCatalogIndexerRole(claims) {
					if ierr := deps.Service.IndexCatalog(ctx, tenant, up, link); ierr != nil {
						logger.Warn("upstream callback: catalog index failed",
							zap.String("tenant", tenant.PublicID),
							zap.String("upstream", upstreamPublicID),
							zap.Error(ierr))
					} else {
						logger.Info("upstream callback: catalog indexed",
							zap.String("tenant", tenant.PublicID),
							zap.String("upstream", upstreamPublicID))
					}
				}
			}
		}

		if returnTo == "" {
			returnTo = "/t/" + tenant.PublicID + "/"
		}
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
	}
}

// handleCallbackError checks if err is an AuthorizationError and renders
// the popup-close page with the error code/description. Returns true if
// the error was handled (rendered), false if it's a different error type.
func (h *UpstreamDeps) handleCallbackError(w http.ResponseWriter, r *http.Request, err error, fallbackMsg string) bool {
	var ae *upstream.AuthorizationError
	if errors.As(err, &ae) && ae.ReturnTo != "" {
		redirectWithOAuthError(w, r, ae.ReturnTo, ae.Code, ae.Description)
		return true
	}
	http.Error(w, fallbackMsg, http.StatusBadRequest)
	return false
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

// loadUserBySubject reads the local User row by zitadel_subject within
// the tenant pinned on ctx. Runs on the tenant-scoped pool; RLS scopes
// the SELECT to the current tenant via app.current_tenant.
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
	if err := tx.Where("zitadel_subject = ?", sub).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("upstream callback: user not found")
		}
		return nil, err
	}
	return &u, nil
}

// redirectWithOAuthError appends Limen's popup-close error params to
// the SPA-supplied returnTo and issues a 303. Mirrors the contract in
// web/shared/src/lib/upstreamOAuthPopup.ts (postOAuthPopupResultAndClose),
// which reads upstream_oauth_error{,_description} from the URL.
func redirectWithOAuthError(w http.ResponseWriter, r *http.Request, returnTo, code, description string) {
	q := url.Values{}
	q.Set("upstream_oauth_error", code)
	if description != "" {
		q.Set("upstream_oauth_error_description", description)
	}
	sep := "?"
	if strings.Contains(returnTo, "?") {
		sep = "&"
	}
	http.Redirect(w, r, returnTo+sep+q.Encode(), http.StatusSeeOther)
}
