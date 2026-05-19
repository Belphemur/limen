// Package zitadelboot constructs the Zitadel management-API client.
// Kept as a sibling of boot (not part of boot itself) so binaries that
// must not reach the Zitadel management surface — notably the MCP
// gateway hot path — never transitively import this package.
package zitadelboot

import (
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/zitadel"
)

// Build returns a configured *zitadel.Client. Best-effort: if the
// admin client can't be built (dev without a PAT, missing project id),
// returns (nil, nil) and emits a warn log — the OAuth proxy / admin
// surfaces silently disable themselves in that case.
func Build(rt *boot.Runtime) (*zitadel.Client, error) {
	zclient, err := zitadel.NewClient(rt.Ctx, zitadel.Config{
		Domain:      rt.Cfg.Zitadel.Domain,
		AuthMode:    zitadel.AuthMode(rt.Cfg.Zitadel.AuthMode),
		PAT:         rt.Cfg.Zitadel.PAT,
		JWTKeyPath:  rt.Cfg.Zitadel.JWTKeyPath,
		ProjectID:   rt.Cfg.Zitadel.ProjectID,
		HTTPTimeout: rt.Cfg.Zitadel.HTTPTimeout,
	})
	if err != nil {
		rt.Logger.Warn("zitadel admin client unavailable", zap.Error(err))
		return nil, nil
	}
	return zclient, nil
}
