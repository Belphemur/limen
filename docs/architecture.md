# Limen Architecture

## System Overview

Limen is an MCP (Model Context Protocol) gateway that aggregates multiple upstream MCP servers behind a single SSE endpoint. Its defining feature is **Code Mode** -- a Goja JavaScript sandbox that collapses all upstream tool definitions into just two meta-tools (`codemode_search` and `codemode_execute`), achieving 94-99% context window reduction compared to traditional tool proxying.

Rather than forwarding every tool schema to the LLM client, Limen inverts the model: the LLM writes JavaScript to discover and invoke only the tools it needs, at the moment it needs them.

## Component Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                         MCP Client                              │
│               (Claude Desktop, Cursor, etc.)                    │
└──────────────────────────┬──────────────────────────────────────┘
                           │
                           │  Single SSE connection
                           │  2 tools: codemode_search, codemode_execute
                           ▼
┌─────────────────────────────────────────────────────────────────┐
│                          Limen                                  │
│                                                                 │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │                    Transport Layer                         │  │
│  │              (internal/transport)                          │  │
│  │                                                            │  │
│  │   chi router → /mcp SSE endpoint (mcp-go SSEServer)       │  │
│  │   Registers codemode_search + codemode_execute tools       │  │
│  └────────────────────────┬──────────────────────────────────┘  │
│                           │                                     │
│                           ▼                                     │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │                   CodeModeHandler                          │  │
│  │                (internal/gateway)                          │  │
│  │                                                            │  │
│  │   Search():  Goja VM → codemode.tools() → filter          │  │
│  │   Execute(): Goja VM → codemode.<toolName>() / .call()    │  │
│  │   Timeout guard, panic recovery, context propagation       │  │
│  └────────────────────────┬──────────────────────────────────┘  │
│                           │                                     │
│                           ▼                                     │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │                       Gateway                              │  │
│  │                (internal/gateway)                          │  │
│  │                                                            │  │
│  │   Tool registry (sync.Map: name → ToolEntry)               │  │
│  │   Upstream router (map: name → UpstreamClient)             │  │
│  │   AddUpstream / RemoveUpstream / CallTool                  │  │
│  └─────┬──────────────┬──────────────┬───────────────────────┘  │
│        │              │              │                          │
│   ┌────▼────┐   ┌─────▼────┐   ┌────▼────┐                     │
│   │ GitHub  │   │   Jira   │   │ Internal│   ← UpstreamClients │
│   │ client  │   │  client  │   │ client  │                     │
│   └────┬────┘   └─────┬────┘   └────┬────┘                     │
└────────┼──────────────┼──────────────┼─────────────────────────┘
         │              │              │
         │  Streamable  │  Streamable  │  Streamable
         │   HTTP/MCP   │   HTTP/MCP   │   HTTP/MCP
         ▼              ▼              ▼
   ┌──────────┐  ┌────────────┐  ┌──────────────┐
   │  GitHub  │  │   Jira     │  │  Internal    │
   │  MCP srv │  │  MCP srv   │  │  MCP srv     │
   └──────────┘  └────────────┘  └──────────────┘
```

## Request Lifecycle

1. **Client connects** to Limen's `/mcp` SSE endpoint via the transport layer.
2. **Tool initialization**: `MCPServer` registers two tools (`codemode_search`, `codemode_execute`) with the `mcp-go` server. The client sees only these two tools.
3. **Discovery phase**: The client calls `codemode_search` with JavaScript code. `CodeModeHandler.Search()` creates a Goja VM, injects all tool definitions as JSON, and exposes `codemode.tools()`. The JS runs and returns a filtered list of relevant tools.
4. **Execution phase**: The client calls `codemode_execute` with JavaScript that invokes tools. `CodeModeHandler.Execute()` creates a fresh Goja VM where each tool is injected as a proxy function on the `codemode` object (e.g., `codemode.jira_get_ticket({id: "PROJ-123"})`). 
5. **Tool proxy**: When JS calls a tool proxy, the handler resolves the tool's upstream, calls `Gateway.CallTool()`, which delegates to the appropriate `MCPUpstreamClient`.
6. **Upstream call**: `MCPUpstreamClient.CallTool()` sends a JSON-RPC `tools/call` request to the upstream server via Streamable HTTP using `mcp-go/client`.
7. **Response propagation**: The upstream response flows back through the gateway → handler → transport → SSE → client, serialized as JSON text content.

## Package Breakdown

### `cmd/gateway` -- Entry Point

The application bootstrap. Responsibilities:

- Parses the `-config` flag for the YAML config path.
- Initializes structured logging via `zap`.
- Loads configuration via `config.Load()`.
- Iterates upstream config, creates `MCPUpstreamClient` instances, connects each, and registers them with the `Gateway`.
- Instantiates `CodeModeHandler` with the execution timeout.
- Wires transport: `NewMCPServer(gateway, handler, logger)` and starts the HTTP/SSE server.
- Handles SIGINT/SIGTERM for graceful shutdown.

### `internal/config` -- Configuration

Defines typed configuration structs and YAML loading:

| Type | Fields |
|------|--------|
| `Config` | `Server`, `Upstreams`, `CodeMode`, `Auth` |
| `ServerConfig` | `Host` (string), `Port` (int) |
| `UpstreamConfig` | `Name` (string), `URL` (string), `Headers` (map[string]string), `Timeout` (time.Duration) |
| `CodeModeConfig` | `ExecutionTimeout` (time.Duration), `MaxMemoryMB` (int) |
| `AuthConfig` | `Enabled` (bool), `JWKSURL` (string), `Audience` (string) |

The `Load()` function reads a YAML file, applies defaults (port: 8080, host: 0.0.0.0, timeout: 30s, memory: 64 MB), and returns a validated `*Config`.

### `internal/gateway` -- Core Gateway

Three files form the core:

**`gateway.go`** -- The `Gateway` orchestrates upstream connections and tool routing.

- **`UpstreamClient` interface**: Contracts for upstream clients (`ListTools`, `CallTool`, `Close`, `Name`).
- **`ToolEntry`**: Represents a discovered tool with `Name`, `Description`, `InputSchema`, and its originating `Upstream`.
- **`Gateway`**: Maintains a `sync.Map` tool registry and a `map[string]UpstreamClient` for routing. Thread-safe via `sync.RWMutex`. Methods: `AddUpstream`, `RemoveUpstream`, `AllTools`, `FindTool`, `CallTool`, `UpstreamNames`, `Close`.

**`codemode.go`** -- The `CodeModeHandler` manages Goia JavaScript sandbox execution.

- **`ToolDefinition`**: Same shape as `ToolEntry` minus the `Upstream` field; used for JS serialization.
- **`Search()`**: Creates a Goja VM, marshals all tool definitions to JSON, injects `codemode.tools()`. Runs the provided JS code.
- **`Execute()`**: Creates a Goja VM, injects each tool as a native JS function on `codemode.<toolName>`, plus `codemode.call(name, args)` for dynamic dispatch. Runs the provided JS code.
- **`runCode()`**: Compiles and executes JS in a goroutine with timeout enforcement via `select` on `time.After` and `ctx.Done()`. Recovers panics and converts them to Go errors.

**`upstream.go`** -- `MCPUpstreamClient` implements the `UpstreamClient` interface.

- Connects to upstream servers via `mcp-go/client`'s `NewStreamableHttpClient` (Streamable HTTP transport).
- Sends MCP `initialize` handshake on `Connect()`.
- `ListTools()` fetches the upstream tool catalog.
- `CallTool()` proxies tool invocations with per-request timeout contexts.
- Applies custom headers (e.g., Authorization) and configurable timeouts.

### `internal/transport` -- HTTP/SSE Server

Exposes the MCP endpoint to LLM clients:

- **`MCPServer`**: Wraps `mcp-go/server.MCPServer` with chi routing.
- **`Start()`**: Creates the MCP server with tool capabilities, registers Code Mode tools, configures a chi router with `/mcp` and `/mcp/` routes pointing to the SSE server, and starts `http.ListenAndServe`.
- **`registerCodeModeTools()`**: Defines `codemode_search` and `codemode_execute` with rich descriptions and examples, then registers them with handlers via `server.AddTool()`.
- **`handleSearch` / `handleExecute`**: Extract the `code` argument from the MCP request, delegate to the handler, and format results as `mcp.CallToolResult` (text content with JSON payload).

### `internal/auth` -- JWT/JWKS Middleware (Stubbed)

Provides the skeleton for HTTP-level authentication:

- **`Middleware`**: Holds JWKS URL and audience configuration.
- **`RequireAuth()`**: HTTP middleware that extracts Bearer tokens and validates them before passing requests downstream.
- **`validateToken()`**: Placeholder -- currently returns an error noting JWKS validation is not yet implemented.
- **`SetClaims` / `GetClaims`**: Context-based claim storage for downstream handlers.

Currently not wired into the HTTP server (see [Roadmap](../README.md#roadmap) in README).

## Key Types

| Type | Package | Role |
|------|---------|------|
| `Gateway` | `gateway` | Central orchestrator; manages upstream clients and tool registry |
| `CodeModeHandler` | `gateway` | Goja JS sandbox; handles `search` and `execute` operations |
| `MCPUpstreamClient` | `gateway` | MCP protocol client for a single upstream server |
| `ToolEntry` | `gateway` | Tool metadata with upstream provenance |
| `ToolDefinition` | `gateway` | Tool metadata for JS serialization (no upstream field) |
| `MCPServer` | `transport` | HTTP/SSE server exposing the MCP endpoint |
| `UpstreamClient` | `gateway` | Interface that all upstream clients must implement |
| `Config` | `config` | Top-level configuration loaded from YAML |

## Dependencies

| Module | Purpose |
|--------|---------|
| `github.com/mark3labs/mcp-go` | MCP protocol implementation (server + client) |
| `github.com/dop251/goja` | JavaScript engine (pure Go) for Code Mode sandbox |
| `github.com/go-chi/chi/v5` | HTTP router for the SSE server |
| `go.uber.org/zap` | Structured logging |
| `gopkg.in/yaml.v3` | YAML config parsing |
