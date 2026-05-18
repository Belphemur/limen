// Package codemodeaction holds the static MCP tool definitions Limen
// advertises to its clients (codemode_search, codemode_execute). The
// definitions are transport-agnostic — internal/transport translates
// them into mcp-go's mcp.Tool at registration time.
//
// IMPORTANT: the Description strings in this package ARE the prompt
// the client LLM sees when picking a tool, so they double as
// user-facing docs. They MUST stay in lock-step with the sandbox API
// implemented in internal/gateway/codemode.go (codemode.tools,
// codemode.schemas, codemode.call, codemode.<upstream>.<name>).
// Whenever you add, remove, or rename a binding — change shape, change
// a quota — update BOTH places in the same change.
package codemodeaction

// Definition describes one MCP tool's static prompt + 'code' argument
// metadata in a transport-agnostic way. Both code-mode tools accept a
// single required string argument named "code", so we model that one
// argument inline rather than introducing a Parameter slice for a
// surface we don't actually use.
type Definition struct {
	// Name is the MCP tool identifier the client invokes (e.g.
	// "codemode_search"). Must match the value advertised by the
	// transport layer; the handler dispatches off it.
	Name string

	// Description is the long-form prompt the client LLM reads when
	// deciding whether to pick this tool. It is the single biggest
	// lever on LLM behaviour — keep it accurate and concise.
	Description string

	// CodeArgDescription is the short docstring for the required
	// "code" argument. Kept separate from Description because some
	// MCP clients surface argument docs in different UI than the tool
	// description (autocomplete vs. tool picker).
	CodeArgDescription string
}
