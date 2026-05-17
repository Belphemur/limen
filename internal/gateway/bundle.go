package gateway

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
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

	c, err := client.NewStreamableHttpClient(
		b.Upstream.McpServerURL,
		transport.WithHTTPBasicClient(b.HTTPClient),
		transport.WithHTTPTimeout(b.Timeout),
	)
	if err != nil {
		return nil, fmt.Errorf("gateway: build client for %q: %w", b.Upstream.Name, err)
	}
	defer func() { _ = c.Close() }()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "limen", Version: "0.1.0"}
	if _, err := c.Initialize(cctx, initReq); err != nil {
		return nil, fmt.Errorf("gateway: initialize %q: %w", b.Upstream.Name, err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args
	resp, err := c.CallTool(cctx, req)
	if err != nil {
		return nil, fmt.Errorf("gateway: call %q.%q: %w", b.Upstream.Name, toolName, err)
	}
	return resp, nil
}
