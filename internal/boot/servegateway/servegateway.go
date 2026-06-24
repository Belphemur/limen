// Package servegateway is the MCP RS hot-path binary entry point used
// by cmd/gateway. It mounts ONLY /t/{tenant}/mcp/* + /healthz + /readyz.
//
// Phase 9a load-bearing constraint: this package and its transitive
// import graph must NOT include internal/oauthproxy or
// internal/zitadel. The MCP gateway is the most internet-exposed,
// highest-traffic binary; it must not carry the credential surface of
// the DCR proxy or the Zitadel management client.
package servegateway

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/boot/mcpmount"
	"github.com/belphemur/limen/internal/mcprs"
)

// Run boots a runtime + MCP-only mux and serves until SIGINT/SIGTERM.
func Run(configPath string) error {
	rt, cleanup, err := boot.BootRuntime(configPath, boot.NeedStore|boot.NeedCipher|boot.NeedUpstream)
	if err != nil {
		return err
	}
	defer cleanup()

	r := chi.NewRouter()
	r.Use(boot.PermissiveCORS)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(boot.RequestLogger(rt.Logger))
	boot.MountHealth(r)

	_, mcpServer, err := mcpmount.Build(rt, nil)
	if err != nil {
		return err
	}
	metadataHandler, err := mcprs.NewHandler(mcprs.MetadataConfig{BaseURL: rt.Cfg.Server.BaseURL})
	if err != nil {
		return err
	}
	mcpAuth, err := auth.NewMCPAuth(rt.Ctx, auth.MCPAuthConfig{
		Issuer:   rt.Cfg.OIDC.Issuer,
		Audience: rt.Cfg.Zitadel.MCPResourceAudience,
	}, metadataHandler, rt.Store, rt.Logger)
	if err != nil {
		return err
	}
	if err := mcpmount.Mount(r, rt, mcpServer, mcpAuth); err != nil {
		return err
	}
	return boot.RunHTTPServer(rt, r)
}
