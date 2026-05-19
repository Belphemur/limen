// Package portalmount attaches the portal SPA + OIDC auth routes.
// Requires a built OIDC RP (see internal/boot/oidcboot). Sibling of
// boot so binaries that don't host the portal (cmd/gateway) never link
// the OIDC RP code transitively.
package portalmount

import (
	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/portal"
	"github.com/belphemur/limen/internal/transport"
)

// Mount wires /t/{tenant}/portal + /t/{tenant}/auth/* + the Phase 9b
// Connect-RPC PortalService under r. apps is the Zitadel client used by
// the MCP client management RPCs (slice 4); pass nil only in non-portal
// binaries that should never see those routes.
func Mount(r chi.Router, rt *boot.Runtime, oidc *auth.OIDC, apps portal.AppManager) {
	svc := portal.NewService(rt.Store, rt.UpstreamService, apps, portal.OIDCSessionResolver(oidc), rt.Logger)
	prefix, handler := svc.Handler()

	transport.MountPortal(r, transport.PortalDeps{
		Store:                 rt.Store,
		OIDC:                  oidc,
		Logger:                rt.Logger,
		PostLogoutRedirectURI: rt.Cfg.OIDC.PostLogoutRedirectURI,
		UpstreamService:       rt.UpstreamService,
		ConnectAPI:            handler,
		ConnectAPIPrefix:      prefix,
	})
}
