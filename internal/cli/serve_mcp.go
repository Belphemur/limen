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
	"github.com/belphemur/limen/internal/gateway/codemode"
	"github.com/belphemur/limen/internal/mcprs"
	"github.com/belphemur/limen/internal/transport"
	"github.com/belphemur/limen/internal/upstream"
)

// setupMCPGateway returns the assembled gateway Manager + MCP
// transport. Per-tenant upstreams are loaded from the database at
// request time (Phase 8); there are no boot-time global upstreams to
// connect here. Requires BootRuntime to have been called with
// NeedUpstream.
func setupMCPGateway(rt *Runtime) (*gateway.Manager, *transport.MCPServer, error) {
	mgr, err := gateway.NewManager(gateway.ManagerOptions{
		Store:    rt.Store,
		Service:  rt.UpstreamService,
		Registry: rt.UpstreamRegistry,
		HealthThresholds: upstream.HealthThresholds{
			FailThreshold:     rt.Cfg.UpstreamRefresh.FailThreshold,
			FailWindow:        rt.Cfg.UpstreamRefresh.FailWindow,
			NeedsRelinkWindow: rt.Cfg.UpstreamRefresh.NeedsRelinkWindow,
		},
		Timeout: rt.Cfg.CodeMode.ExecutionTimeout,
		Logger:  rt.Logger,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build gateway manager: %w", err)
	}
	cmHandler := codemode.New(gateway.CodemodeDispatcher{Manager: mgr}, codemode.Config{
		ScriptTimeout:          rt.Cfg.CodeMode.ScriptTimeout,
		MaxToolCalls:           rt.Cfg.CodeMode.MaxToolCalls,
		MaxConcurrentToolCalls: rt.Cfg.CodeMode.MaxConcurrentToolCalls,
	}, rt.Logger)
	mcpServer := transport.NewMCPServer(mgr, cmHandler, rt.Logger)
	return mgr, mcpServer, nil
}

// mountMCPResource attaches the MCP Resource Server under
// /t/{tenant}/mcp/*. Builds the PRM handler first, then the
// access-token middleware (which performs OIDC discovery against the
// configured issuer to fetch jwks_uri at startup).
func mountMCPResource(r chi.Router, rt *Runtime, mcpServer *transport.MCPServer) error {
	metadataHandler, err := mcprs.NewHandler(mcprs.MetadataConfig{
		BaseURL: rt.Cfg.Server.BaseURL,
	})
	if err != nil {
		return fmt.Errorf("build mcp resource metadata: %w", err)
	}
	mcpAuth, err := auth.NewMCPAuth(rt.Ctx, auth.MCPAuthConfig{
		Issuer:   rt.Cfg.OIDC.Issuer,
		Audience: rt.Cfg.Zitadel.MCPResourceAudience,
	}, metadataHandler, rt.Store, rt.Logger)
	if err != nil {
		return fmt.Errorf("build mcp auth: %w", err)
	}
	if err := transport.MountMCPRS(r, transport.MCPRSDeps{
		Store:     rt.Store,
		MCPServer: mcpServer,
		MCPAuth:   mcpAuth,
		Metadata:  metadataHandler,
		Logger:    rt.Logger,
	}); err != nil {
		return fmt.Errorf("mount mcp resource server: %w", err)
	}
	return nil
}
