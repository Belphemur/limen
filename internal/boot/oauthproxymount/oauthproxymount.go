// Package oauthproxymount attaches the OAuth proxy routes (DCR + AS
// metadata + redirector). Requires a built *zitadel.Client. Sibling of
// boot so binaries that don't host the OAuth proxy (cmd/gateway) never
// link internal/oauthproxy or internal/zitadel transitively.
package oauthproxymount

import (
	"fmt"

	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/boot"
	oauthtransport "github.com/belphemur/limen/internal/oauthproxy/transport"
	"github.com/belphemur/limen/internal/zitadel"
)

// Mount attaches the OAuth proxy routes. Best-effort: when z is nil,
// the proxy is silently skipped (the Zitadel admin client wasn't
// available at boot — dev posture).
func Mount(r chi.Router, rt *boot.Runtime, z *zitadel.Client) error {
	if z == nil {
		rt.Logger.Warn("oauth proxy disabled: zitadel admin client unavailable")
		return nil
	}
	if err := oauthtransport.Mount(r, oauthtransport.Deps{
		Store:          rt.Store,
		Zitadel:        z,
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
