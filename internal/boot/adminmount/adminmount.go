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

	"github.com/belphemur/limen/internal/admin"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/tenant"
)

// NewHandler returns the URL-path prefix + http.Handler pair for
// AdminService. The caller composes it onto /t/{tenant}/api/ via an
// http.ServeMux alongside PortalService and SessionService.
//
// tenant.Service lives here (rather than on boot.Runtime) so the MCP
// gateway hot-path binary does not transitively pull in
// internal/oauthproxy via internal/tenant's redirect-URI validator —
// see cmd/gateway/import_graph_test.go.
func NewHandler(rt *boot.Runtime, resolver session.Resolver) (string, http.Handler) {
	tenantSvc := tenant.NewService(rt.Store)
	svc := admin.NewService(rt.Store, rt.UpstreamService, tenantSvc, resolver, rt.Logger)
	return svc.Handler()
}
