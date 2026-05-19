// Package cli — OAuth proxy suite.
//
// Owns the AS-metadata + DCR proxy + redirector routes under
// /t/{tenant}/oauth/* that let MCP clients perform RFC 7591 dynamic
// client registration against Zitadel via Limen.
package cli

import (
	"fmt"

	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/transport"
)

// mountOAuthProxy attaches the OAuth proxy routes. Best-effort: if the
// Zitadel admin client wasn't built at boot (dev without a PAT, missing
// project id, etc.), the proxy is silently skipped and the rest of the
// binary still serves.
func mountOAuthProxy(r chi.Router, rt *Runtime) error {
	if rt.Zitadel == nil {
		rt.Logger.Warn("oauth proxy disabled: zitadel admin client unavailable")
		return nil
	}
	if err := transport.MountOAuthProxy(r, transport.OAuthProxyDeps{
		Store:          rt.Store,
		Zitadel:        rt.Zitadel,
		Logger:         rt.Logger,
		BaseURL:        rt.Cfg.Server.BaseURL,
		Issuer:         rt.Cfg.OIDC.Issuer,
		MCPRSProjectID: rt.Cfg.Zitadel.MCPResourceAudience,
		OAuthCfg:       rt.Cfg.OAuthProxy,
	}); err != nil {
		return fmt.Errorf("mount oauth proxy: %w", err)
	}
	return nil
}
