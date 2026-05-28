// Package transport — code-mode MCP server.
//
// This file is the downstream-facing MCP server that Limen exposes to
// clients. It is a thin shell: it does not aggregate or proxy upstream
// tools directly. Instead it advertises exactly two tools —
// codemode_search and codemode_execute — and delegates their execution
// to codemode.Handler, which runs tenant-supplied JavaScript in
// an isolated sandbox with the per-user upstream tool catalog injected.
//
// All real fan-out (per-tenant upstream lookup, per-user auth header
// injection, link-health bookkeeping, resilience) lives behind the
// handler, on gateway.Manager. From this file's perspective there is one
// MCP server per process; tenant scoping is recovered at request time
// from the chi route via tenancy.TenantFromContext, which the dynamic
// base-path callback uses to advertise the correct per-tenant message
// endpoint.
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/gateway/codemode"
	"github.com/belphemur/limen/internal/gateway/codemodeaction"
	"github.com/belphemur/limen/internal/tenancy"
)

// MCPServer is the code-mode MCP server. It wraps a single mcp-go SSE
// server configured with a dynamic base path so one instance can serve
// every tenant: the base path is derived from the request's resolved
// tenant and the server advertises per-tenant message endpoints under
// /t/{tenant}/mcp/message.
//
// The advertised tool surface is fixed: codemode_search (discover) and
// codemode_execute (call). Upstream tools are never exposed directly —
// the client reaches them by writing JavaScript that calls codemode.*
// inside the handler's sandbox.
type MCPServer struct {
	manager    *gateway.Manager
	handler    *codemode.Handler
	logger     *zap.Logger
	core       *server.MCPServer
	sse        *server.SSEServer
	streamable *server.StreamableHTTPServer
}

// NewMCPServer constructs the code-mode MCP server. All tool execution
// flows through handler; the Manager passed to codemode.New is
// what actually fans out to per-(tenant, upstream) Bundles — this
// transport layer only sees the handler facade.
func NewMCPServer(manager *gateway.Manager, handler *codemode.Handler, logger *zap.Logger) *MCPServer {
	s := &MCPServer{
		manager: manager,
		handler: handler,
		logger:  logger,
	}
	s.core = server.NewMCPServer(
		"limen",
		"0.1.0",
		server.WithToolCapabilities(true),
	)
	s.registerCodeModeTools()
	s.sse = server.NewSSEServer(
		s.core,
		server.WithDynamicBasePath(func(r *http.Request, _ string) string {
			if t, ok := tenancy.TenantFromContext(r.Context()); ok {
				return "/t/" + t.PublicID + "/mcp"
			}
			return "/mcp"
		}),
	)
	s.streamable = server.NewStreamableHTTPServer(
		s.core,
		server.WithStateLess(true),
	)
	return s
}

// SSEHandler returns the long-lived event-stream handler clients GET to
// open an MCP session. Mount at the tenant subroute path "/sse"; the
// dynamic base-path callback rewrites the advertised message endpoint
// to /t/{tenant}/mcp/message before sending it to the client.
func (s *MCPServer) SSEHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if rec := s.manager.BillingRecorder(); rec != nil {
			if sa, ok := auth.MCPServiceAccountFromContext(ctx); ok {
				if tenant, ok := tenancy.TenantFromContext(ctx); ok {
					rec.RecordSAConnection(ctx, tenant.ID, sa.ID, true)
				}
			}
		}
		s.sse.SSEHandler().ServeHTTP(w, r)
		// TODO: record disconnect when the SSE connection closes.
		// The UPSERT logic in the billing consumer handles stale
		// connection counts; a clean disconnect hook would reduce
		// reconciliation latency.
	})
}

// MessageHandler returns the JSON-RPC POST handler that ingests client
// requests for an existing SSE session. Mount at the tenant subroute
// path "/message".
func (s *MCPServer) MessageHandler() http.Handler { return s.sse.MessageHandler() }

// StreamableHandler returns the Streamable HTTP transport handler (MCP
// 2025-03-26 spec). It accepts POST for JSON-RPC requests and GET for
// optional server-initiated streaming. Mount at the tenant subroute
// root so clients can POST to /t/{tenant}/mcp directly — most modern
// MCP clients (Cursor, Claude Desktop) probe streamable HTTP before
// falling back to legacy SSE.
func (s *MCPServer) StreamableHandler() http.Handler { return s.streamable }

// Core exposes the underlying mcp-go server for callers that need to
// register additional tools after construction. Intended for tests and
// future extension points; production wiring should not bypass the
// code-mode surface.
func (s *MCPServer) Core() *server.MCPServer { return s.core }

// registerCodeModeTools advertises the fixed downstream tool surface:
// codemode_search (read-only discovery of the per-user tool catalog)
// and codemode_execute (sandboxed JS that calls upstream tools via
// codemode.<upstream>.<name>). The verbose descriptions are the prompt
// the client LLM sees when picking a tool, so they double as user-facing
// docs.
//
// IMPORTANT: this prompt MUST stay in lock-step with the sandbox API
// implemented in internal/gateway/codemode.go. Whenever you add,
// remove, or rename a codemode.* binding, change the listing/schema
// shape, or tweak quotas, update BOTH files in the same change — the
// LLM only knows the API surface described here.
//
// The static prompt + argument metadata lives in
// internal/gateway/tools so it stays out of this transport-shaped file.
// This function is the single place that turns those definitions into
// mcp-go's mcp.Tool and wires the matching handler.
func (s *MCPServer) registerCodeModeTools() {
	s.core.AddTool(buildTool(codemodeaction.Search), s.handleSearch)
	s.core.AddTool(buildTool(codemodeaction.Execute), s.handleExecute)
}

// buildTool converts a static codemodeaction.Definition into the
// mcp.Tool that mcp-go's server registers. Both code-mode tools share
// the same shape (one required string argument named "code"), so one
// helper covers the whole surface.
func buildTool(def codemodeaction.Definition) mcp.Tool {
	return mcp.NewTool(def.Name,
		mcp.WithDescription(def.Description),
		mcp.WithString("code",
			mcp.Required(),
			mcp.Description(def.CodeArgDescription),
		),
	)
}

// handleSearch runs the supplied JS through codemode.Handler.Search,
// which exposes only codemode.tools() — no upstream dispatch. Argument
// validation and handler errors are surfaced as MCP error results
// (IsError=true) rather than transport-level errors so the client LLM
// can react to them programmatically.
func (s *MCPServer) handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	code, ok := req.GetArguments()["code"].(string)
	if !ok {
		return errorResult("code argument must be a string"), nil
	}

	s.logger.Debug("codemode_search: received script",
		zap.Int("script_bytes", len(code)),
		zap.String("script", code))

	result, err := s.handler.Search(ctx, code)
	if err != nil {
		s.logger.Debug("codemode_search: handler error", zap.Error(err))
		return errorResult(fmt.Sprintf("search failed: %v", err)), nil
	}

	s.logger.Debug("codemode_search: handler result",
		zap.String("result_json", marshalForDebug(result)))
	return successResult(result), nil
}

// handleExecute runs the supplied JS through codemode.Handler.Execute,
// which exposes codemode.tools() plus per-tool proxies bound to the
// caller's tenant + user context. Same error-shaping rule as Search.
func (s *MCPServer) handleExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	code, ok := req.GetArguments()["code"].(string)
	if !ok {
		return errorResult("code argument must be a string"), nil
	}

	s.logger.Debug("codemode_execute: received script",
		zap.Int("script_bytes", len(code)),
		zap.String("script", code))

	result, err := s.handler.Execute(ctx, code)
	if err != nil {
		s.logger.Debug("codemode_execute: handler error", zap.Error(err))
		return errorResult(fmt.Sprintf("execute failed: %v", err)), nil
	}

	s.logger.Debug("codemode_execute: handler result",
		zap.String("result_json", marshalForDebug(result)))
	return successResult(result), nil
}

// marshalForDebug JSON-encodes v for debug-only logging. Encoding errors
// fall back to a Go fmt %#v dump so the log line is never empty.
func marshalForDebug(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<marshal error: %v> %#v", err, v)
	}
	return string(b)
}

// errorResult wraps a human-readable message as an MCP tool error. The
// MCP protocol distinguishes transport errors (returned as the second
// return value) from in-band tool errors (IsError=true on a successful
// result); we use the latter so the calling LLM stays in control.
func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: msg},
		},
		IsError: true,
	}
}

// successResult JSON-encodes the handler's return value as a single
// text content block. Encoding errors are swallowed because the handler
// already constrains data to JSON-serializable shapes.
func successResult(data any) *mcp.CallToolResult {
	jsonBytes, _ := json.Marshal(data)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: string(jsonBytes)},
		},
	}
}
