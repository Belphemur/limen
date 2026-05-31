// Package mcpmount builds the MCP gateway Manager + Resource Server
// transport and mounts the /t/{tenant}/mcp/* routes. Imported by every
// binary that serves the MCP hot path (cmd/gateway, cmd/limen).
package mcpmount

import (
	"fmt"

	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/billing/metrics"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/gateway/codemode"
	"github.com/belphemur/limen/internal/mcprs"
	"github.com/belphemur/limen/internal/transport"
	"github.com/belphemur/limen/internal/upstream"
)

// Build returns the assembled gateway Manager + MCP transport.
// Requires boot.NeedStore + NeedUpstream in the profile.
func Build(rt *boot.Runtime) (*gateway.Manager, *transport.MCPServer, error) {
	billingRecorder := metrics.NewBillingRecorder(rt.Valkey, rt.Store, rt.Logger.Named("billing-recorder"))
	if !billingRecorder.Enabled() {
		billingRecorder.StartFallbackDrain(rt.Ctx)
	}
	rt.AddCleanup(billingRecorder.Close)
	mgr, err := gateway.NewManager(gateway.ManagerOptions{
		Store:    rt.Store,
		Service:  rt.UpstreamService,
		Registry: rt.UpstreamRegistry,
		HealthThresholds: upstream.HealthThresholds{
			FailThreshold:     rt.Cfg.UpstreamRefresh.FailThreshold,
			FailWindow:        rt.Cfg.UpstreamRefresh.FailWindow,
			NeedsRelinkWindow: rt.Cfg.UpstreamRefresh.NeedsRelinkWindow,
		},
		Timeout:          rt.Cfg.CodeMode.ExecutionTimeout,
		Logger:           rt.Logger,
		ResiliencePolicy: rt.Cfg.Resilience.Resolve("upstream.tool_calls"),
		ValkeyClient:     rt.Valkey,
		BillingRecorder:  billingRecorder,
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

// Mount attaches the MCP Resource Server under /t/{tenant}/mcp/*.
// The caller must supply a pre-built MCPAuth (which performs OIDC
// discovery against the configured issuer to fetch jwks_uri at startup).
func Mount(r chi.Router, rt *boot.Runtime, mcpServer *transport.MCPServer, mcpAuth *auth.MCPAuth) error {
	metadataHandler, err := mcprs.NewHandler(mcprs.MetadataConfig{
		BaseURL: rt.Cfg.Server.BaseURL,
	})
	if err != nil {
		return fmt.Errorf("build mcp resource metadata: %w", err)
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
