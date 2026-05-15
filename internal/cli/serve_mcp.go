// Package cli — MCP suite.
//
// Owns the downstream-facing MCP machinery: the gateway that aggregates
// configured upstream MCP servers, the code-mode handler, and the MCP
// Resource Server routes under /t/{tenant}/mcp/* (PRM document +
// access-token middleware + JSON-RPC transport).
package cli

import (
	"fmt"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/mcprs"
	"github.com/belphemur/limen/internal/transport"
)

// setupMCPGateway connects every configured downstream MCP upstream and
// returns the assembled gateway + MCP transport. Connection failures
// are logged and skipped — the portal is still useful even if a single
// upstream is unreachable at boot.
func setupMCPGateway(d *serverDeps) (*gateway.Gateway, *transport.MCPServer) {
	gw := gateway.New(d.logger)
	for _, uc := range d.cfg.Upstreams {
		client := gateway.NewMCPUpstream(uc.Name, uc.URL, uc.Headers, uc.Timeout, d.logger)
		if err := client.Connect(d.ctx); err != nil {
			d.logger.Error("failed to connect upstream",
				zap.String("name", uc.Name),
				zap.Error(err))
			continue
		}
		if err := gw.AddUpstream(d.ctx, client); err != nil {
			d.logger.Error("failed to add upstream",
				zap.String("name", uc.Name),
				zap.Error(err))
			_ = client.Close()
			continue
		}
	}
	cmHandler := gateway.NewCodeModeHandler(gw, d.logger, d.cfg.CodeMode.ExecutionTimeout)
	mcpServer := transport.NewMCPServer(gw, cmHandler, d.logger)
	return gw, mcpServer
}

// mountMCPResource attaches the MCP Resource Server under
// /t/{tenant}/mcp/*. Builds the PRM handler first, then the
// access-token middleware (which performs OIDC discovery against the
// configured issuer to fetch jwks_uri at startup).
func mountMCPResource(r chi.Router, d *serverDeps, mcpServer *transport.MCPServer) error {
	metadataHandler, err := mcprs.NewHandler(mcprs.MetadataConfig{
		BaseURL: d.cfg.Server.BaseURL,
	})
	if err != nil {
		return fmt.Errorf("build mcp resource metadata: %w", err)
	}
	mcpAuth, err := auth.NewMCPAuth(d.ctx, auth.MCPAuthConfig{
		Issuer:   d.cfg.OIDC.Issuer,
		Audience: d.cfg.Zitadel.MCPResourceAudience,
	}, metadataHandler, d.store, d.logger)
	if err != nil {
		return fmt.Errorf("build mcp auth: %w", err)
	}
	if err := transport.MountMCPRS(r, transport.MCPRSDeps{
		Store:     d.store,
		MCPServer: mcpServer,
		MCPAuth:   mcpAuth,
		Metadata:  metadataHandler,
		Logger:    d.logger,
	}); err != nil {
		return fmt.Errorf("mount mcp resource server: %w", err)
	}
	return nil
}
