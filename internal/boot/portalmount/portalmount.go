// Package portalmount attaches the portal SPA + OIDC auth routes.
// Requires a built OIDC RP (see internal/boot/oidcboot). Sibling of
// boot so binaries that don't host the portal (cmd/gateway) never link
// the OIDC RP code transitively.
package portalmount

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/boot/sessionmount"
	"github.com/belphemur/limen/internal/portal"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/transport"
)

// Mount wires /t/{tenant}/portal + /t/{tenant}/auth/* + the Phase 9b
// Connect-RPC PortalService under r. apps is the Zitadel client used by
// the MCP client management RPCs (slice 4); pass nil only in non-portal
// binaries that should never see those routes.
//
// PortalService and SessionService share the same /t/{tenant}/api/
// mount point — they're multiplexed via an http.ServeMux keyed on the
// Connect procedure prefix.
func Mount(r chi.Router, rt *boot.Runtime, oidc *auth.OIDC, apps portal.AppManager) {
	resolver := session.OIDCResolver(oidc)

	portalSvc := portal.NewService(rt.Store, rt.UpstreamService, apps, resolver, rt.Logger)
	portalPrefix, portalHandler := portalSvc.Handler()

	sessPrefix, sessHandler := sessionmount.NewHandler(rt, resolver)

	// http.ServeMux dispatches on longest-prefix match without
	// stripping the prefix from r.URL.Path — exactly what Connect
	// expects for its procedure-path routing.
	api := http.NewServeMux()
	api.Handle(portalPrefix, portalHandler)
	api.Handle(sessPrefix, sessHandler)

	transport.MountPortal(r, transport.PortalDeps{
		Store:                 rt.Store,
		OIDC:                  oidc,
		Logger:                rt.Logger,
		PostLogoutRedirectURI: rt.Cfg.OIDC.PostLogoutRedirectURI,
		ConnectAPI:            api,
	})
}
