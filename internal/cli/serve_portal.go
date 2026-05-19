// Package cli — portal suite mount helper.
package cli

import (
	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/transport"
)

// mountPortal attaches the portal SPA + OIDC auth routes.
func mountPortal(r chi.Router, rt *Runtime) {
	transport.MountPortal(r, transport.PortalDeps{
		Store:                 rt.Store,
		OIDC:                  rt.OIDC,
		Logger:                rt.Logger,
		PostLogoutRedirectURI: rt.Cfg.OIDC.PostLogoutRedirectURI,
		UpstreamService:       rt.UpstreamService,
	})
}
