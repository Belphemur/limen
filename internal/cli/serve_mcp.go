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

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/mcprs"
	"github.com/belphemur/limen/internal/transport"
)

// setupMCPGateway returns the assembled gateway + MCP transport. Per-tenant
// upstreams are loaded from the database at request time (Phase 8); there
// are no boot-time global upstreams to connect here.
func setupMCPGateway(d *serverDeps) (*gateway.Gateway, *transport.MCPServer) {
	gw := gateway.New(d.logger)
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
