package gateway

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"
)

type ToolEntry struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
	Upstream    string                 `json:"upstream"`
}

type UpstreamClient interface {
	ListTools(ctx context.Context) ([]ToolEntry, error)
	CallTool(ctx context.Context, name string, args map[string]interface{}) (interface{}, error)
	Close() error
	Name() string
}

type Gateway struct {
	logger    *zap.Logger
	upstreams map[string]UpstreamClient
	tools     sync.Map // name -> ToolEntry
	mu        sync.RWMutex
}

func New(logger *zap.Logger) *Gateway {
	return &Gateway{
		logger:    logger,
		upstreams: make(map[string]UpstreamClient),
	}
}

func (g *Gateway) AddUpstream(ctx context.Context, client UpstreamClient) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	name := client.Name()
	if _, exists := g.upstreams[name]; exists {
		return fmt.Errorf("upstream %q already registered", name)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("failed to list tools from %s: %w", name, err)
	}

	g.upstreams[name] = client

	for _, tool := range tools {
		tool.Upstream = name
		g.tools.Store(tool.Name, tool)
	}

	g.logger.Info("registered upstream",
		zap.String("name", name),
		zap.Int("tools", len(tools)))

	return nil
}

func (g *Gateway) RemoveUpstream(ctx context.Context, name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	client, exists := g.upstreams[name]
	if !exists {
		return fmt.Errorf("upstream %q not found", name)
	}

	g.tools.Range(func(key, value any) bool {
		if tool, ok := value.(ToolEntry); ok && tool.Upstream == name {
			g.tools.Delete(key)
		}
		return true
	})

	delete(g.upstreams, name)
	client.Close()

	g.logger.Info("removed upstream", zap.String("name", name))
	return nil
}

func (g *Gateway) AllTools() []ToolEntry {
	var tools []ToolEntry
	g.tools.Range(func(key, value any) bool {
		if tool, ok := value.(ToolEntry); ok {
			tools = append(tools, tool)
		}
		return true
	})
	return tools
}

func (g *Gateway) FindTool(name string) (ToolEntry, bool) {
	val, ok := g.tools.Load(name)
	if !ok {
		return ToolEntry{}, false
	}
	return val.(ToolEntry), true
}

func (g *Gateway) CallTool(ctx context.Context, upstreamName, toolName string, args map[string]interface{}) (interface{}, error) {
	g.mu.RLock()
	client, exists := g.upstreams[upstreamName]
	g.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("upstream %q not found", upstreamName)
	}

	return client.CallTool(ctx, toolName, args)
}

func (g *Gateway) UpstreamNames() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	names := make([]string, 0, len(g.upstreams))
	for name := range g.upstreams {
		names = append(names, name)
	}
	return names
}

func (g *Gateway) Close() error {
	var firstErr error
	for name, client := range g.upstreams {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("error closing %s: %w", name, err)
		}
	}
	return firstErr
}
