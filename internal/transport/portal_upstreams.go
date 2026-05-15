// Package transport — portal PoC upstream endpoints.
//
// These four JSON routes give the Phase 4 portal HTML PoC enough surface
// to drive Phase 7's StartConnect / FinishCallback / Disconnect /
// SubmitUpstreamAPIKey end-to-end from a browser. Mounted under the
// existing /t/{tenant}/portal subrouter, so they inherit RequireTenant
// + OIDC RequireSession.
//
// This is intentionally a stop-gap. Phase 9 replaces it with a typed
// Connect-RPC service. We are pre-1.0 — keep the shape light, the
// status enum stable, and don't bother with backwards-compat shims when
// Phase 9 lands.
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
)

// upstreamListItem is the per-row payload returned by GET /portal/upstreams.
type upstreamListItem struct {
	Name         string `json:"name"`
	PublicID     string `json:"public_id"`
	Strategy     string `json:"strategy"`
	McpServerURL string `json:"mcp_server_url"`
	Status       string `json:"status"`
	NeedsRelink  bool   `json:"needs_relink,omitempty"`
	AutoDisabled bool   `json:"auto_disabled,omitempty"`
	Enabled      bool   `json:"enabled"`
}

// linkStatus collapses (link presence + flags) into a single label the
// SPA renders verbatim. Mirrors the Phase 7 health doc.
const (
	statusDisconnected = "disconnected"
	statusConnected    = "connected"
	statusNeedsRelink  = "needs_relink"
	statusAutoDisabled = "auto_disabled"
	statusDisabled     = "disabled"
)

func mountPortalUpstreams(pr chi.Router, deps PortalDeps) {
	if deps.UpstreamService == nil {
		return
	}
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	pr.Get("/upstreams", listUpstreamsHandler(deps, logger))
	pr.Post("/upstreams/{name}/connect", connectUpstreamHandler(deps, logger))
	pr.Post("/upstreams/{name}/disconnect", disconnectUpstreamHandler(deps, logger))
}

func listUpstreamsHandler(deps PortalDeps, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tenant, user, ok := resolvePortalCaller(ctx, w, deps, logger)
		if !ok {
			return
		}
		ups, err := deps.UpstreamService.ListUpstreams(ctx, tenant.ID)
		if err != nil {
			logger.Warn("portal: list upstreams failed", zap.Error(err))
			http.Error(w, "list failed", http.StatusInternalServerError)
			return
		}
		items := make([]upstreamListItem, 0, len(ups))
		for i := range ups {
			up := &ups[i]
			item := upstreamListItem{
				Name:         up.Name,
				PublicID:     up.PublicID,
				Strategy:     up.StrategyType,
				McpServerURL: up.McpServerURL,
				Status:       statusDisconnected,
				Enabled:      true,
			}
			link, lerr := deps.UpstreamService.LoadLink(ctx, tenant.ID, user.ID, up.ID)
			switch {
			case errors.Is(lerr, upstream.ErrLinkNotFound):
				// disconnected — defaults are fine
			case lerr != nil:
				logger.Warn("portal: load link failed",
					zap.String("upstream", up.Name), zap.Error(lerr))
				item.Status = "error"
			default:
				item.Status = linkStatus(link)
				item.Enabled = link.Enabled
				item.NeedsRelink = link.NeedsRelink
				item.AutoDisabled = link.AutoDisabledAt != nil
			}
			items = append(items, item)
		}
		writeJSON(w, http.StatusOK, items)
	}
}

func connectUpstreamHandler(deps PortalDeps, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tenant, user, ok := resolvePortalCaller(ctx, w, deps, logger)
		if !ok {
			return
		}
		name := chi.URLParam(r, "name")
		returnTo := "/t/" + tenant.PublicID + "/portal/"
		redirectURL, err := deps.UpstreamService.StartConnect(ctx, tenant, user, name, returnTo)
		if err != nil {
			logger.Warn("portal: start connect failed",
				zap.String("upstream", name), zap.Error(err))
			status := http.StatusBadRequest
			if errors.Is(err, upstream.ErrUpstreamNotFound) {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"redirect_url": redirectURL,
			"linked":       strings.TrimSpace(redirectURL) == "",
		})
	}
}

func disconnectUpstreamHandler(deps PortalDeps, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tenant, user, ok := resolvePortalCaller(ctx, w, deps, logger)
		if !ok {
			return
		}
		name := chi.URLParam(r, "name")
		if err := deps.UpstreamService.Disconnect(ctx, tenant, user, name); err != nil {
			logger.Warn("portal: disconnect failed",
				zap.String("upstream", name), zap.Error(err))
			status := http.StatusBadRequest
			if errors.Is(err, upstream.ErrUpstreamNotFound) {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// resolvePortalCaller is the boilerplate every handler runs: pull the
// tenant from the route middleware, the OIDC subject from the session,
// and look up the local user row.
func resolvePortalCaller(ctx context.Context, w http.ResponseWriter, deps PortalDeps, logger *zap.Logger) (*storage.Tenant, *storage.User, bool) {
	tenant, ok := tenancy.TenantFromContext(ctx)
	if !ok {
		http.Error(w, "tenant required", http.StatusBadRequest)
		return nil, nil, false
	}
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		http.Error(w, "auth required", http.StatusUnauthorized)
		return nil, nil, false
	}
	user, err := loadUserBySubject(ctx, deps.Store, tenant.ID, claims.GetSubject())
	if err != nil {
		logger.Warn("portal: user lookup failed",
			zap.String("tenant", tenant.PublicID), zap.Error(err))
		http.Error(w, "user not found", http.StatusForbidden)
		return nil, nil, false
	}
	return tenant, user, true
}

func linkStatus(link *storage.UpstreamLink) string {
	switch {
	case link.AutoDisabledAt != nil:
		return statusAutoDisabled
	case link.NeedsRelink:
		return statusNeedsRelink
	case !link.Enabled:
		return statusDisabled
	default:
		return statusConnected
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
