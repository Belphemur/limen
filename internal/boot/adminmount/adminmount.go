// Package adminmount builds the AdminService Connect handler for
// binaries that host the admin SPA backend (portal, all-in-one).
// Sibling of internal/boot/portalmount + sessionmount; the handler is
// composed onto the same /t/{tenant}/api/ mount via http.ServeMux.
//
// cmd/gateway intentionally does NOT call into this package — gateway
// is bearer-token only and never accepts the portal cookie.
package adminmount

import (
	"net/http"

	"connectrpc.com/connect"

	"github.com/belphemur/limen/internal/admin"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/tenant"
)

// members is the Zitadel directory pass-through used by the
// Members tab (List/Invite/UpdateRole/Remove). Pass nil to leave
// those RPCs returning CodeUnimplemented.
//
// tenant.Service lives here (rather than on boot.Runtime) so the MCP
// gateway hot-path binary does not transitively pull in
// internal/oauthproxy via internal/tenant's redirect-URI validator —
// see cmd/gateway/import_graph_test.go.
func NewHandler(rt *boot.Runtime, resolver session.Resolver, bearerIntercept, billingIntercept connect.UnaryInterceptorFunc, members admin.MemberDirectory, serviceAccounts admin.ServiceAccountDirectory) (string, http.Handler) {
	tenantSvc := tenant.NewService(rt.Store)
	svc := admin.NewService(rt.Store, rt.UpstreamService, tenantSvc, resolver, bearerIntercept, billingIntercept, members, serviceAccounts, rt.Cfg.Zitadel.Domain, rt.Cfg.Zitadel.ProjectID, admin.OIDCCredentials{
		ClientID:     rt.Cfg.OIDC.ClientID,
		ClientSecret: rt.Cfg.OIDC.ClientSecret,
	}, rt.Cipher, rt.Cfg.Security.PortalSessionCookieSecure, rt.Logger)
	return svc.Handler()
}
