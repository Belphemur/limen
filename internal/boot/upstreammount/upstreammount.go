// Package upstreammount attaches the OAuth /callback route under
// /t/{tenant}<server.upstream_callback_path>/{name}/callback (default
// segment "/mcp-servers"). Requires a built OIDC RP (for the
// portal-session middleware that gates the callback) and the
// upstream.Service populated by boot.NeedUpstream.
package upstreammount

import (
	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/transport"
)

// Mount attaches the OAuth callback route. No-op when upstream linking
// was disabled at boot (Valkey not configured).
func Mount(r chi.Router, rt *boot.Runtime, oidc *auth.OIDC) {
	if rt.UpstreamService == nil {
		return
	}
	transport.MountUpstream(r, transport.UpstreamDeps{
		Store:        rt.Store,
		OIDC:         oidc,
		Service:      rt.UpstreamService,
		Logger:       rt.Logger,
		CallbackPath: rt.Cfg.Server.UpstreamCallbackPath,
	})
}
