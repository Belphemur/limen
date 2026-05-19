// Package cli — outbound upstream linking mount helper.
//
// Boot-time wiring (strategy registry, OAuth state store, Service,
// refresher) lives in BootRuntime / bootUpstream; this file only
// attaches the OAuth /callback HTTP route.
package cli

import (
	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/transport"
)

// mountUpstreamLinking attaches the OAuth /callback route. No-op when
// upstream linking was disabled at boot (Valkey not configured).
func mountUpstreamLinking(r chi.Router, rt *Runtime) {
	if rt.UpstreamService == nil {
		return
	}
	transport.MountUpstream(r, transport.UpstreamDeps{
		Store:   rt.Store,
		OIDC:    rt.OIDC,
		Service: rt.UpstreamService,
		Logger:  rt.Logger,
	})
}
