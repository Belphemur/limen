package gateway

import (
	"context"

	"github.com/belphemur/limen/internal/gateway/codemode"
)

// CodemodeDispatcher adapts *Manager into a codemode.Dispatcher by
// projecting the gateway-internal ToolEntry shape into the leaner
// codemode.Tool. Lives in the gateway package (not codemode) so the
// codemode package stays leaf-level — it must not import gateway.
type CodemodeDispatcher struct {
	Manager *Manager
}

// ToolsForUser projects []ToolEntry → []codemode.Tool.
func (a CodemodeDispatcher) ToolsForUser(ctx context.Context) ([]codemode.Tool, error) {
	entries, err := a.Manager.ToolsForUser(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]codemode.Tool, len(entries))
	for i, e := range entries {
		out[i] = codemode.Tool{
			Name:        e.Name,
			Description: e.Description,
			Upstream:    e.Upstream,
			InputSchema: e.InputSchema,
		}
	}
	return out, nil
}

// CallTool delegates to the underlying Manager.
func (a CodemodeDispatcher) CallTool(ctx context.Context, upstream, name string, args map[string]any) (any, error) {
	return a.Manager.CallTool(ctx, upstream, name, args)
}

// UpstreamsForUser projects []UpstreamView → []codemode.UpstreamMeta.
func (a CodemodeDispatcher) UpstreamsForUser(ctx context.Context) ([]codemode.UpstreamMeta, error) {
	views, err := a.Manager.UpstreamsForUser(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]codemode.UpstreamMeta, len(views))
	for i, v := range views {
		out[i] = codemode.UpstreamMeta{
			Name:    v.Name,
			Aliases: v.Aliases,
			Context: v.Context,
		}
	}
	return out, nil
}
