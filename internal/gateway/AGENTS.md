# AGENTS.md — `internal/gateway`

## What this package is

The core MCP gateway. Two responsibilities:

1. **Aggregate upstream MCP servers** into a single virtual MCP endpoint:
   discover their tools at startup, route incoming tool calls to the right
   upstream, surface upstream errors back to the client.
2. **Run Code Mode** — a Goja JavaScript sandbox that lets operators compose
   tools server-side. The sandbox has _no_ ambient capability: filesystem,
   network, and `eval` are all off; only explicitly injected tool functions
   are callable.

## File layout

| Path                  | Purpose                                                                       |
| --------------------- | ----------------------------------------------------------------------------- |
| `manager.go`          | `Manager`: per-(tenant, upstream) Bundle cache + request-time fan-out.        |
| `bundle.go`           | One Bundle = one live upstream connection scoped to a (tenant, upstream).     |
| `authtransport.go`    | Outbound HTTP RoundTripper that injects per-user upstream auth headers.       |
| `types.go`            | `ToolEntry` — the gateway-internal shape Manager hands to callers.            |
| `codemode_adapter.go` | `CodemodeDispatcher` — bridges `*Manager` → `codemode.Dispatcher`.            |
| `codemode/`           | Subpackage: the Goja sandbox. See lock-step note below.                       |
| `codemodeaction/`     | Subpackage: the two MCP tool definitions (`codemode_search`, `_execute`).     |

### Lock-step: `codemode/` ↔ `codemodeaction/`

These two subpackages MUST be changed together. The `codemodeaction` package
owns the **prompt surface** (the human-readable tool descriptions and JSON
schemas that the LLM sees and writes JS against); the `codemode` package
owns the **runtime surface** (the actual `codemode.tools()`,
`codemode.schemas()`, `codemode.<upstream>.<tool>()`, `codemode.call()`
implementations the script invokes).

Any change to one of:

- the shape of `codemode.tools(filter)` (filter keys, return-list fields),
- the shape/arguments of `codemode.schemas(names)`,
- the property-chain or `codemode.call(upstream, name, args)` semantics,
- reserved keys on the `codemode` namespace,
- sandbox denials,

requires a matching edit in the other package's prompt copy
(`codemodeaction/search.go`, `codemodeaction/execute.go`) — otherwise the
LLM will keep writing scripts against the old API.

The codemode package is leaf-level: it does **not** import gateway. The
gateway-side `CodemodeDispatcher` adapter is the only bridge.

## Boundaries

- HTTP handlers (in `internal/transport`) stay thin: parse the request,
  invoke a `Gateway` method, write the response. Business logic lives here.
- The gateway does **not** read config directly — `cmd/gateway` injects a
  resolved `*config.Config` (or sub-section) into the constructor.
- Errors returned from upstreams are wrapped with `%w` and surfaced verbatim
  through MCP; do not swallow them.

## Conventions

- New upstreams are registered through `gateway.NewGateway(...)`. Adding a
  protocol (stdio, WebSocket, etc.) means adding a transport in `upstream.go`,
  not branching inside dispatch.
- Tool name collisions across upstreams are resolved by prefixing with the
  upstream name (see `gateway.go`). Do not change this rule silently —
  client-side bindings depend on it.
- Code Mode injects each tool as a plain function on the JS runtime's global
  object. Never expose `os`, `process`, `fetch`, or filesystem helpers.
- Long-running calls must respect `cfg.CodeMode.ExecutionTimeout` and the
  request context. The sandbox is single-threaded — a goroutine leak inside
  Goja blocks the whole gateway.

## Multi-tenancy hooks (Phases 4-8)

Phase 1 establishes the storage layer; Phases 4-8 wire the gateway into it:

- Per-tenant upstream lists will come from `internal/storage` rather than
  `config.yaml`.
- Per-user upstream tokens (the `UpstreamLink` table) will be injected into
  outbound requests by `upstream.go` after Phase 7.
- The auth middleware (`internal/auth`) pins the tenant into ctx via
  `storage.WithTenant`; the gateway must propagate that ctx unchanged into
  every upstream call.

## What this package is NOT

- Not an authorization decider. Roles, scopes, and per-tenant policy live in
  `internal/auth` (Phase 5/6).
- Not a persistence layer. Anything that needs to survive a restart goes
  through `internal/storage`.
- Not a transport. SSE/HTTP framing lives in `internal/transport`.
