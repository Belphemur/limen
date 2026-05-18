// Package transport's mcprs.go wires the Phase 6 MCP Resource Server
// endpoints under /t/{tenant}/mcp/*.
//
// Layout:
//
//	GET  /t/{tenant}/mcp/.well-known/oauth-protected-resource  (public PRM)
//	GET  /t/{tenant}/mcp/sse        (SSE stream, bearer required)
//	POST /t/{tenant}/mcp/message    (JSON-RPC ingest, bearer required)
//
// All routes run behind tenancy.RequireTenant. The PRM endpoint is
// intentionally public — RFC 9728 §3 mandates discovery without
// authentication.
package transport

import (
	"errors"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/mcprs"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

// MCPRSDeps bundles everything MountMCPRS needs.
type MCPRSDeps struct {
	Store     *storage.Store
	MCPServer *MCPServer
	MCPAuth   *auth.MCPAuth
	Metadata  *mcprs.Handler
	Logger    *zap.Logger
}

// MountMCPRS attaches the MCP RS routes onto r.
func MountMCPRS(r chi.Router, deps MCPRSDeps) error {
	if deps.Store == nil {
		return errors.New("transport: MCPRS Store is required")
	}
	if deps.MCPServer == nil {
		return errors.New("transport: MCPRS MCPServer is required")
	}
	if deps.MCPAuth == nil {
		return errors.New("transport: MCPRS MCPAuth is required")
	}
	if deps.Metadata == nil {
		return errors.New("transport: MCPRS Metadata is required")
	}
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	r.Route("/t/{tenant}/mcp", func(mr chi.Router) {
		mr.Use(tenancy.RequireTenant(deps.Store, logger))

		mr.Get(mcprs.MetadataPath, deps.Metadata.ServeHTTP)

		mr.Group(func(ar chi.Router) {
			ar.Use(deps.MCPAuth.RequireMCPAuth)
			ar.Handle("/sse", deps.MCPServer.SSEHandler())
			ar.Handle("/message", deps.MCPServer.MessageHandler())
			// Streamable HTTP (MCP 2025-03-26) — clients POST JSON-RPC
			// to the tenant base URL itself. mcp-go's handler dispatches
			// on method (POST/GET/DELETE) so a single mount serves all.
			ar.Handle("/", deps.MCPServer.StreamableHandler())
		})
	})
	return nil
}
