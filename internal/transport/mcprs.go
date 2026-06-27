// Package transport's mcprs.go wires the Phase 6 MCP Resource Server
// endpoints under /t/{tenant}/mcp/*.
//
// Layout:
//
//	GET  /t/{tenant}/mcp/.well-known/oauth-protected-resource  (public PRM)
//	GET  /.well-known/oauth-protected-resource/t/{tenant}/mcp  (public PRM, RFC 9728 §3.2)
//	GET  /t/{tenant}/mcp/sse        (SSE stream, bearer required, NOT billing-gated)
//	GET  /t/{tenant}/mcp/           (Streamable HTTP notifications, bearer required, NOT billing-gated)
//	POST /t/{tenant}/mcp/message    (JSON-RPC ingest, bearer required, billing-gated)
//	POST /t/{tenant}/mcp/           (Streamable HTTP requests, bearer required, billing-gated)
//
// All routes run behind tenancy.RequireTenant. The PRM endpoint is
// intentionally public — RFC 9728 §3 mandates discovery without
// authentication.
//
// SSE and the Streamable-HTTP GET stream are deliberately split OUT
// of the billing-gated group: they are read-only transport plumbing,
// and a past-due tenant's tool-calling LLM still needs to be able to
// open the event stream to receive the in-band
// `notifications/billing_warning` the billing middleware appends to
// other responses. The only state-changing or upstream-bound
// endpoints (POST /message, POST /) sit behind BillingMiddleware.
package transport

import (
	"errors"
	"net/http"

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
	// BillingMiddleware, when non-nil, gates the auth-protected
	// POST endpoints (message ingest + Streamable HTTP) on the
	// tenant's subscription lifecycle state. Mounted AFTER
	// RequireMCPAuth (anonymous traffic is rejected first) and BEFORE
	// the MCP handlers so the in-band JSON-RPC error / warning the
	// middleware produces is what the client sees. SSE is
	// intentionally not wrapped — see file header. May be nil; the
	// auth+billing group is still constructed but without the billing
	// middleware.
	BillingMiddleware func(http.Handler) http.Handler
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

		// Auth-only group — SSE is the long-lived event stream. The
		// billing gate never blocks it: a past-due tenant's LLM must
		// still be able to open the stream to receive the in-band
		// notifications/billing_warning notifications that get
		// appended to the POST responses.
		mr.Group(func(ar chi.Router) {
			ar.Use(deps.MCPAuth.RequireMCPAuth)
			ar.Handle("/sse", deps.MCPServer.SSEHandler())
		})

		// Auth + billing group — POST endpoints. These are the only
		// paths that touch upstream tools or session state, so they
		// are the only ones that need the lifecycle gate. The
		// billing middleware in this slot is constructed by the
		// serve* binaries as RequireBillingActiveMCP so the verdict
		// surfaces as a JSON-RPC error (block) or notification
		// (grace) rather than an HTTP 402.
		//
		// Per MCP 2025-03-26 §2.1.2 the Streamable HTTP transport
		// exposes a long-lived GET on the same path that posts
		// JSON-RPC. Mounting the handler with a method-agnostic
		// Handle("/", ...) would gate GET behind the billing
		// middleware too, which would break a past-due tenant's
		// LLM from receiving the in-band billing-warning
		// notifications that the middleware appends to the POST
		// responses — exactly the channel a blocked tenant
		// needs to recover. So we register the same Streamable
		// handler twice: POST under the billing gate, GET outside
		// it. Both still require a valid bearer (the group-level
		// RequireMCPAuth above).
		mr.Group(func(ar chi.Router) {
			ar.Use(deps.MCPAuth.RequireMCPAuth)
			if deps.BillingMiddleware == nil {
				ar.Handle("/message", deps.MCPServer.MessageHandler())
				ar.Handle("/", deps.MCPServer.StreamableHandler())
				return
			}
			ar.With(deps.BillingMiddleware).Handle("POST /message", deps.MCPServer.MessageHandler())
			ar.With(deps.BillingMiddleware).Handle("POST /", deps.MCPServer.StreamableHandler())
			ar.Handle("GET /", deps.MCPServer.StreamableHandler())
		})
	})

	// RFC 9728 §3.2 host-root PRM discovery for strict MCP clients
	// (mcp-inspector, MCP TypeScript SDK, Claude Desktop). The
	// well-known URI sits between the origin and the resource path
	// so the server can publish metadata without the client knowing
	// the full resource-path prefix ahead of time.
	r.Route("/.well-known/oauth-protected-resource/t/{tenant}/mcp", func(wr chi.Router) {
		wr.Use(tenancy.RequireTenant(deps.Store, logger))
		wr.Get("/", deps.Metadata.ServeHTTP)
	})
	return nil
}
