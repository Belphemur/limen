// Package cli — OAuth proxy suite.
//
// Owns the AS-metadata + DCR proxy + redirector routes under
// /t/{tenant}/oauth/* that let MCP clients perform RFC 7591 dynamic
// client registration against Zitadel via Limen.
package cli

import (
	"fmt"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/transport"
	"github.com/belphemur/limen/internal/zitadel"
)

// mountOAuthProxy attaches the OAuth proxy routes. Best-effort: if the
// Zitadel admin client can't be built (dev without a PAT, missing
// project id, etc.), the proxy is silently skipped and the rest of the
// gateway still boots.
func mountOAuthProxy(r chi.Router, d *serverDeps) error {
	zclient, zerr := zitadel.NewClient(d.ctx, zitadel.Config{
		Domain:      d.cfg.Zitadel.Domain,
		AuthMode:    zitadel.AuthMode(d.cfg.Zitadel.AuthMode),
		PAT:         d.cfg.Zitadel.PAT,
		JWTKeyPath:  d.cfg.Zitadel.JWTKeyPath,
		ProjectID:   d.cfg.Zitadel.ProjectID,
		HTTPTimeout: d.cfg.Zitadel.HTTPTimeout,
	})
	if zerr != nil {
		d.logger.Warn("oauth proxy disabled: zitadel admin client unavailable", zap.Error(zerr))
		return nil
	}
	if err := transport.MountOAuthProxy(r, transport.OAuthProxyDeps{
		Store:    d.store,
		Zitadel:  zclient,
		Logger:   d.logger,
		BaseURL:  d.cfg.Server.BaseURL,
		Issuer:   d.cfg.OIDC.Issuer,
		OAuthCfg: d.cfg.OAuthProxy,
	}); err != nil {
		return fmt.Errorf("mount oauth proxy: %w", err)
	}
	return nil
}
