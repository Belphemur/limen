// Package portalmount attaches the portal SPA + OIDC auth routes.
// Requires a built OIDC RP (see internal/boot/oidcboot). Sibling of
// boot so binaries that don't host the portal (cmd/gateway) never link
// the OIDC RP code transitively.
package portalmount

import (
	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/transport"
)

// Mount wires /t/{tenant}/portal + /t/{tenant}/auth/* under r.
func Mount(r chi.Router, rt *boot.Runtime, oidc *auth.OIDC) {
	transport.MountPortal(r, transport.PortalDeps{
		Store:                 rt.Store,
		OIDC:                  oidc,
		Logger:                rt.Logger,
		PostLogoutRedirectURI: rt.Cfg.OIDC.PostLogoutRedirectURI,
		UpstreamService:       rt.UpstreamService,
	})
}
