package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/tenancy"
)

// MCPServer wraps a single mcp-go SSE server configured with a dynamic
// base path. The base path is derived from the request's resolved tenant
// so a single server instance correctly advertises per-tenant message
// endpoints under /t/{tenant}/mcp/message.
type MCPServer struct {
	gateway *gateway.Gateway
	handler *gateway.CodeModeHandler
	logger  *zap.Logger
	core    *server.MCPServer
	sse     *server.SSEServer
}

func NewMCPServer(gw *gateway.Gateway, handler *gateway.CodeModeHandler, logger *zap.Logger) *MCPServer {
	s := &MCPServer{
		gateway: gw,
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
	return s
}

// SSEHandler returns the SSE stream handler. Mount at the tenant subroute
// path "/sse".
func (s *MCPServer) SSEHandler() http.Handler { return s.sse.SSEHandler() }

// MessageHandler returns the JSON-RPC message ingestion handler. Mount at
// the tenant subroute path "/message".
func (s *MCPServer) MessageHandler() http.Handler { return s.sse.MessageHandler() }

// Core exposes the underlying mcp-go server for callers that need to
// register additional tools after construction.
func (s *MCPServer) Core() *server.MCPServer { return s.core }

func (s *MCPServer) registerCodeModeTools() {
	searchTool := mcp.NewTool("codemode_search",
		mcp.WithDescription(`Discover available tools across all connected MCP servers.

This tool lets you write JavaScript to search, filter, and explore the capabilities
of all upstream MCP servers without loading their full schemas into your context.

The JavaScript code runs in a sandbox with a "codemode" object available.
Your code MUST be an async arrow function: async () => { ... }

Available in the sandbox:
  codemode.tools() - Returns array of all tool definitions from all upstreams.
    Each tool has: { name, description, inputSchema, upstream }
    inputSchema follows JSON Schema format with properties, required, type fields.

Example - find all Jira-related tools:
  async () => {
    const tools = await codemode.tools();
    return tools.filter(t => t.name.includes("jira"));
  }

Example - find tools that accept a "projectId" parameter:
  async () => {
    const tools = await codemode.tools();
    return tools.filter(t =>
      t.inputSchema.properties &&
      t.inputSchema.properties.projectId
    );
  }

Example - list all unique upstream names:
  async () => {
    const tools = await codemode.tools();
    return [...new Set(tools.map(t => t.upstream))];
  }

After discovering tools, use codemode_execute to call them.`),
		mcp.WithString("code",
			mcp.Required(),
			mcp.Description("An async arrow function that uses codemode.tools() to discover and filter available tools. Example: async () => { const tools = await codemode.tools(); return tools.filter(t => t.name.includes('jira')); }"),
		),
	)

	executeTool := mcp.NewTool("codemode_execute",
		mcp.WithDescription(`Execute JavaScript code that calls tools from connected MCP servers.

This tool lets you call any discovered tool directly from JavaScript. Each tool
is available as a method on the "codemode" object, named exactly as it appears
in the tool catalog (e.g., codemode.jira_get_ticket, codemode.github_search).

The JavaScript code runs in a sandbox with a "codemode" object available.
Your code MUST be an async arrow function: async () => { ... }

Available in the sandbox:
  codemode.<toolName>(args) - Call a tool directly. Args is an object matching
    the tool's inputSchema.properties. Returns the tool's result.
    Example: codemode.jira_get_ticket({ id: "PROJ-123" })

  codemode.call(toolName, args) - Call a tool by name string. Useful when
    the tool name has special characters or is dynamically determined.
    Example: codemode.call("jira_get_ticket", { id: "PROJ-123" })

  codemode.tools() - Returns all tool definitions (same as codemode_search).

You can chain multiple tool calls, handle errors with try/catch, and compose
results. All calls happen in a single execution.

Example - get a Jira ticket and update it with GitHub info:
  async () => {
    const ticket = await codemode.jira_get_ticket({ id: "PROJ-123" });
    const pr = await codemode.github_get_pr({ number: 456 });
    await codemode.jira_update_ticket({
      id: "PROJ-123",
      description: ticket.description + "\n\nPR: " + pr.title
    });
    return { updated: "PROJ-123" };
  }

Example - list repos then get issues from each:
  async () => {
    const repos = await codemode.github_list_repos({ org: "myorg" });
    const issues = [];
    for (const repo of repos.slice(0, 3)) {
      const list = await codemode.github_list_issues({ repo: repo.name });
      issues.push({ repo: repo.name, count: list.length });
    }
    return issues;
  }

Use codemode_search first to discover tool names and their argument schemas.`),
		mcp.WithString("code",
			mcp.Required(),
			mcp.Description("An async arrow function that calls tools via codemode.<toolName>(args). Example: async () => { return await codemode.jira_get_ticket({ id: 'PROJ-123' }); }"),
		),
	)

	s.core.AddTool(searchTool, s.handleSearch)
	s.core.AddTool(executeTool, s.handleExecute)
}

func (s *MCPServer) handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	code, ok := req.GetArguments()["code"].(string)
	if !ok {
		return errorResult("code argument must be a string"), nil
	}

	result, err := s.handler.Search(ctx, code)
	if err != nil {
		return errorResult(fmt.Sprintf("search failed: %v", err)), nil
	}

	return successResult(result), nil
}

func (s *MCPServer) handleExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	code, ok := req.GetArguments()["code"].(string)
	if !ok {
		return errorResult("code argument must be a string"), nil
	}

	result, err := s.handler.Execute(ctx, code)
	if err != nil {
		return errorResult(fmt.Sprintf("execute failed: %v", err)), nil
	}

	return successResult(result), nil
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: msg},
		},
		IsError: true,
	}
}

func successResult(data any) *mcp.CallToolResult {
	jsonBytes, _ := json.Marshal(data)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: string(jsonBytes)},
		},
	}
}
