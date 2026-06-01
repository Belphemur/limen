// Package portalmount attaches the portal SPA + OIDC auth routes.
// Requires a built OIDC RP (see internal/boot/oidcboot). Sibling of
// boot so binaries that don't host the portal (cmd/gateway) never link
// the OIDC RP code transitively.
package portalmount

import (
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/admin"
	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/boot/adminmount"
	"github.com/belphemur/limen/internal/boot/sessionmount"
	"github.com/belphemur/limen/internal/boot/signupmount"
	"github.com/belphemur/limen/internal/portal"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/signup"
	"github.com/belphemur/limen/internal/transport"
)

// Mount wires /t/{tenant}/portal + /t/{tenant}/auth/* + the
// Connect-RPC PortalService, SessionService, and AdminService under r,
// plus the tenant-agnostic SignupService + /auth/discovery at the
// root. apps is the Zitadel client used by the MCP client management
// RPCs (Phase 9b slice 4); pass nil only in non-portal binaries that
// should never see those routes. members is the Zitadel directory
// pass-through used by the Members tab (List/Invite/UpdateRole/Remove);
// the same *zitadel.Client also satisfies admin.MemberDirectory.
// signupZitadel is the Zitadel client SignupService uses to provision
// new orgs; the same *zitadel.Client also satisfies signup.ZitadelClient.
//
// PortalService, SessionService, and AdminService share the same
// /t/{tenant}/api/ mount point — they're multiplexed via an
// http.ServeMux keyed on the Connect procedure prefix. SignupService
// is tenant-agnostic and lives at /api/limen.signup.v1.SignupService/*.
//
// Returns the API mux (*http.ServeMux) and the constructed *signup.Service
// so the binary can register additional Connect handlers (e.g. BillingService)
// and launch the background sweeper goroutine on its lifetime. Returns an
// error when SignupService construction fails (template load, mailer
// build, captcha provider invalid).
func Mount(r chi.Router, rt *boot.Runtime, oidc *auth.OIDC, bearerIntercept connect.UnaryInterceptorFunc, apps portal.AppManager, members admin.MemberDirectory, serviceAccounts admin.ServiceAccountDirectory, signupZitadel signup.ZitadelClient, resolver session.Resolver) (*http.ServeMux, *signup.Service, error) {
	portalSvc := portal.NewService(rt.Store, rt.UpstreamService, apps, resolver, bearerIntercept, rt.Logger)
	portalPrefix, portalHandler := portalSvc.Handler()

	sessPrefix, sessHandler := sessionmount.NewHandler(rt, resolver, bearerIntercept)
	adminPrefix, adminHandler := adminmount.NewHandler(rt, resolver, bearerIntercept, members, serviceAccounts)

	// http.ServeMux dispatches on longest-prefix match without
	// stripping the prefix from r.URL.Path — exactly what Connect
	// expects for its procedure-path routing.
	api := http.NewServeMux()
	api.Handle(portalPrefix, portalHandler)
	api.Handle(sessPrefix, sessHandler)
	api.Handle(adminPrefix, adminHandler)

	signupPrefix, signupHandler, signupSvc, err := signupmount.NewHandler(rt, signupZitadel)
	if err != nil {
		return nil, nil, err
	}
	signupAPI := http.NewServeMux()
	signupAPI.Handle(signupPrefix, signupHandler)

	transport.MountPortal(r, transport.PortalDeps{
		Store:                 rt.Store,
		OIDC:                  oidc,
		Logger:                rt.Logger,
		PostLogoutRedirectURI: rt.Cfg.OIDC.PostLogoutRedirectURI,
		OIDCIssuer:            rt.Cfg.OIDC.Issuer,
		CaptchaProvider:       rt.Cfg.Captcha.Provider,
		CaptchaSiteKey:        rt.Cfg.Captcha.SiteKey,
		ConnectAPI:            api,
		SignupAPI:             signupAPI,
	})
	return api, signupSvc, nil
}
