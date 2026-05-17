// Package cli — MCP suite.
//
// Owns the downstream-facing MCP machinery: the gateway Manager that
// builds per-(tenant, upstream) Bundles on demand, the code-mode
// handler, and the MCP Resource Server routes under /t/{tenant}/mcp/*
// (PRM document + access-token middleware + JSON-RPC transport).
package cli

import (
	"fmt"

	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/mcprs"
	"github.com/belphemur/limen/internal/transport"
	"github.com/belphemur/limen/internal/upstream"
)

// setupMCPGateway returns the assembled gateway Manager + MCP
// transport. Per-tenant upstreams are loaded from the database at
// request time (Phase 8); there are no boot-time global upstreams to
// connect here. Requires setupUpstreamLinking to have populated
// d.upstreamService + d.upstreamRegistry.
func setupMCPGateway(d *serverDeps) (*gateway.Manager, *transport.MCPServer, error) {
	mgr, err := gateway.NewManager(gateway.ManagerOptions{
		Store:    d.store,
		Service:  d.upstreamService,
		Registry: d.upstreamRegistry,
		HealthThresholds: upstream.HealthThresholds{
			FailThreshold:     d.cfg.UpstreamRefresh.FailThreshold,
			FailWindow:        d.cfg.UpstreamRefresh.FailWindow,
			NeedsRelinkWindow: d.cfg.UpstreamRefresh.NeedsRelinkWindow,
		},
		Timeout: d.cfg.CodeMode.ExecutionTimeout,
		Logger:  d.logger,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build gateway manager: %w", err)
	}
	cmHandler := gateway.NewCodeModeHandler(mgr, gateway.CodeModeConfig{
		ScriptTimeout: d.cfg.CodeMode.ScriptTimeout,
		MaxToolCalls:  d.cfg.CodeMode.MaxToolCalls,
	}, d.logger)
	mcpServer := transport.NewMCPServer(mgr, cmHandler, d.logger)
	return mgr, mcpServer, nil
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
