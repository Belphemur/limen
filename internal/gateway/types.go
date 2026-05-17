// Package gateway is the request-time fan-out layer between the
// downstream MCP server (transport.MCPServer) and the per-(tenant,
// upstream) Bundles built lazily by Manager.
//
// The legacy boot-time aggregator (Gateway + UpstreamClient + global
// tool index) was removed in Phase 8: there is no longer a single
// global view of upstream tools — every catalog and every dispatch is
// scoped to (tenant, user) at request time.
//
// This file declares the package-shared ToolEntry shape that Manager
// and CodeModeHandler exchange.
package gateway

// ToolEntry is the gateway-facing representation of a single upstream
// MCP tool. Manager materializes these from the upstream_tools catalog
// at request time, filtered to the tools the calling (tenant, user) is
// authorized to see.
type ToolEntry struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Upstream    string         `json:"upstream"`
}
