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
	"github.com/belphemur/limen/internal/boot/adminmount"
	"github.com/belphemur/limen/internal/boot/sessionmount"
	"github.com/belphemur/limen/internal/boot/signupmount"
	"github.com/belphemur/limen/internal/portal"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/transport"
)

// Mount wires /t/{tenant}/portal + /t/{tenant}/auth/* + the
// Connect-RPC PortalService, SessionService, and AdminService under r,
// plus the tenant-agnostic SignupService + /auth/discovery at the
// root. apps is the Zitadel client used by the MCP client management
// RPCs (Phase 9b slice 4); pass nil only in non-portal binaries that
// should never see those routes.
//
// PortalService, SessionService, and AdminService share the same
// /t/{tenant}/api/ mount point — they're multiplexed via an
// http.ServeMux keyed on the Connect procedure prefix. SignupService
// is tenant-agnostic and lives at /api/limen.signup.v1.SignupService/*.
func Mount(r chi.Router, rt *boot.Runtime, oidc *auth.OIDC, apps portal.AppManager) {
	resolver := session.OIDCResolver(oidc)

	portalSvc := portal.NewService(rt.Store, rt.UpstreamService, apps, resolver, rt.Logger)
	portalPrefix, portalHandler := portalSvc.Handler()

	sessPrefix, sessHandler := sessionmount.NewHandler(rt, resolver)
	adminPrefix, adminHandler := adminmount.NewHandler(rt, resolver)

	// http.ServeMux dispatches on longest-prefix match without
	// stripping the prefix from r.URL.Path — exactly what Connect
	// expects for its procedure-path routing.
	api := http.NewServeMux()
	api.Handle(portalPrefix, portalHandler)
	api.Handle(sessPrefix, sessHandler)
	api.Handle(adminPrefix, adminHandler)

	signupPrefix, signupHandler := signupmount.NewHandler(rt)
	signupAPI := http.NewServeMux()
	signupAPI.Handle(signupPrefix, signupHandler)

	transport.MountPortal(r, transport.PortalDeps{
		Store:                 rt.Store,
		OIDC:                  oidc,
		Logger:                rt.Logger,
		PostLogoutRedirectURI: rt.Cfg.OIDC.PostLogoutRedirectURI,
		OIDCIssuer:            rt.Cfg.OIDC.Issuer,
		ConnectAPI:            api,
		SignupAPI:             signupAPI,
	})
}
