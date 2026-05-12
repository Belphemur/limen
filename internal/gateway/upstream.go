package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type MCPUpstreamClient struct {
	name    string
	url     string
	headers map[string]string
	client  *client.Client
	logger  *zap.Logger
	timeout time.Duration
}

func NewMCPUpstream(name, url string, headers map[string]string, timeout time.Duration, logger *zap.Logger) *MCPUpstreamClient {
	return &MCPUpstreamClient{
		name:    name,
		url:     url,
		headers: headers,
		logger:  logger,
		timeout: timeout,
	}
}

func (u *MCPUpstreamClient) Connect(ctx context.Context) error {
	opts := []transport.StreamableHTTPCOption{
		transport.WithHTTPTimeout(u.timeout),
	}
	if len(u.headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(u.headers))
	}

	c, err := client.NewStreamableHttpClient(u.url, opts...)
	if err != nil {
		return fmt.Errorf("failed to create MCP client for %s: %w", u.name, err)
	}

	ctx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "limen",
		Version: "0.1.0",
	}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		return fmt.Errorf("failed to initialize MCP connection to %s: %w", u.name, err)
	}

	u.client = c
	u.logger.Info("connected to upstream", zap.String("name", u.name), zap.String("url", u.url))
	return nil
}

func (u *MCPUpstreamClient) ListTools(ctx context.Context) ([]ToolEntry, error) {
	if u.client == nil {
		return nil, fmt.Errorf("upstream %s not connected", u.name)
	}

	ctx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()

	resp, err := u.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list tools from %s: %w", u.name, err)
	}

	tools := make([]ToolEntry, 0, len(resp.Tools))
	for _, t := range resp.Tools {
		inputSchema := make(map[string]any)
		if schemaBytes, err := json.Marshal(t.InputSchema); err == nil {
			json.Unmarshal(schemaBytes, &inputSchema)
		}

		tools = append(tools, ToolEntry{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: inputSchema,
			Upstream:    u.name,
		})
	}

	return tools, nil
}

func (u *MCPUpstreamClient) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	if u.client == nil {
		return nil, fmt.Errorf("upstream %s not connected", u.name)
	}

	ctx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()

	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args

	resp, err := u.client.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tool call failed on %s: %w", u.name, err)
	}

	return resp.Content, nil
}

func (u *MCPUpstreamClient) Name() string {
	return u.name
}

func (u *MCPUpstreamClient) Close() error {
	if u.client != nil {
		u.client.Close()
	}
	return nil
}
