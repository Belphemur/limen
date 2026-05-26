// Package sessionmount builds the shared SessionService Connect handler
// for binaries that expose a SPA-facing API (portal, staff, all-in-one).
// Sibling of internal/boot/portalmount so the handler can be composed
// onto the same /t/{tenant}/api/ mount point as service-specific
// Connect handlers via http.ServeMux.
//
// cmd/gateway intentionally does NOT call into this package — the
// gateway is bearer-token only and never accepts the portal cookie.
package sessionmount

import (
	"net/http"

	"connectrpc.com/connect"

	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/session"
)

// NewHandler returns the URL-path prefix + http.Handler pair for the
// shared SessionService. The caller is responsible for placing the
// returned handler behind tenancy.RequireTenant (typically via
// transport.MountPortal's /api mount).
func NewHandler(rt *boot.Runtime, resolver session.Resolver, impersonationResolver session.Resolver, bearerIntercept connect.UnaryInterceptorFunc) (string, http.Handler) {
	svc := session.NewService(resolver, impersonationResolver, bearerIntercept, rt.Logger)
	return svc.Handler()
}
