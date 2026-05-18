package gateway

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
)

// Bundle binds one (tenant, upstream) to the wiring needed to talk to
// the upstream MCP server on behalf of an arbitrary user on the request
// ctx. The mcp-go streamable client is *not* held long-lived here — it is
// constructed per call from the cached *http.Client whose Transport is
// the AuthInjectingTransport. That keeps the Bundle stateless w.r.t. the
// upstream's session: every CallTool runs Initialize + tools/call against
// a fresh session, so two users of the same upstream never share session
// state at the MCP layer.
type Bundle struct {
	Tenant     *storage.Tenant
	Upstream   *storage.Upstream
	Strategy   upstream.Strategy
	Auth       upstream.AuthProvider
	HTTPClient *http.Client
	Logger     *zap.Logger
	Timeout    time.Duration
}

// CallTool builds an ephemeral mcp-go client, runs Initialize, and
// dispatches tools/call. The HTTPClient's AuthInjectingTransport injects
// the bearer for the user on ctx and records per-link health.
func (b *Bundle) CallTool(ctx context.Context, toolName string, args map[string]any) (*mcp.CallToolResult, error) {
	cctx, cancel := context.WithTimeout(ctx, b.Timeout)
	defer cancel()

	c, err := upstream.DialAndInitialize(cctx, b.Upstream.McpServerURL, nil, b.HTTPClient, b.Timeout, "limen", "0.1.0")
	if err != nil {
		return nil, fmt.Errorf("gateway: dial %q: %w", b.Upstream.Name, err)
	}
	defer func() { _ = c.Close() }()

	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args
	resp, err := c.CallTool(cctx, req)
	if err != nil {
		return nil, fmt.Errorf("gateway: call %q.%q: %w", b.Upstream.Name, toolName, err)
	}
	return resp, nil
}
