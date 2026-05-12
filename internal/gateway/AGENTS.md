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

| File          | Purpose                                                             |
| ------------- | ------------------------------------------------------------------- |
| `gateway.go`  | `Gateway` type; lifecycle (start/stop), tool aggregation, dispatch. |
| `upstream.go` | MCP upstream client (HTTP/SSE) — one per configured upstream.       |
| `codemode.go` | Goja runtime, tool injection, execution-timeout + memory caps.      |

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
