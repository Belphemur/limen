# Limen

> The MCP Gateway

Aggregate multiple upstream MCP servers behind a single endpoint, using **Code Mode** to collapse all tool definitions into 2 meta-tools -- achieving 94-99% context window reduction.

## Architecture

```
MCP Client (Claude, Cursor, etc.)
        │
        │  connects to ONE endpoint
        ▼
┌─────────────────────────────────────────────┐
│                 Limen                       │
│                                             │
│  ┌─────────────────────────────────────┐    │
│  │       Code Mode Handler             │    │
│  │                                     │    │
│  │  Exposes 2 tools only:              │    │
│  │  ┌─────────────────────────────┐    │    │
│  │  │ codemode_search             │    │    │
│  │  │ codemode_execute            │    │    │
│  │  └─────────────────────────────┘    │    │
│  │                                     │    │
│  │  ┌─────────────────────────────┐    │    │
│  │  │   Goja JS Sandbox           │    │    │
│  │  │   codemode.tools()          │    │    │
│  │  │   codemode.call()           │    │    │
│  │  │   codemode.toolName()       │    │    │
│  │  └─────────────────────────────┘    │    │
│  └─────────────────────────────────────┘    │
│                                             │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │ GitHub   │  │  Jira    │  │ Internal │  │
│  │ upstream │  │ upstream │  │ upstream │  │
│  └──────────┘  └──────────┘  └──────────┘  │
└─────────────────────────────────────────────┘
```

## How Code Mode Works

Traditional MCP gateways proxy every tool schema to the LLM, burning tokens on definitions the agent doesn't need. Limen's Code Mode inverts this: instead of sending all tool schemas upfront, it exposes just **two meta-tools** and lets the LLM use JavaScript to discover and call only what it needs.

### Discovery

The agent calls `codemode_search` with a JavaScript filter function to find relevant tools:

```js
async () => {
  const tools = await codemode.tools();
  return tools.filter(t => t.name.includes("jira"));
}
```

This returns a small subset of matching tools -- no full schema dump.

### Execution

The agent calls `codemode_execute` with JavaScript that invokes tools directly:

```js
async () => {
  const ticket = await codemode.jira_get_ticket({ id: "PROJ-123" });
  const doc = await codemode.github_get_file({ path: "README.md" });
  return { ticket, doc };
}
```

Each upstream tool is injected as a native JS function on the `codemode` object. The agent calls them naturally -- no tool name string matching, no argument schema parsing.

### Token Impact

| Scenario | Tokens |
|----------|--------|
| Code Mode (2 meta-tools) | ~600 |
| No Code Mode (52 tools across 4 servers) | ~9,400 |

That's a **94-99% reduction** in context window consumption, regardless of how many upstream tools are aggregated.

## Quick Start

```bash
# Build every binary (limen, limenctl, limen-gateway, limen-portal, limen-staff)
make build

# Initialise the database (one-shot)
./limenctl migrate --config config.yaml

# Run the all-in-one
./limen serve --config config.yaml
```

For production, run `limen-gateway`, `limen-portal`, and `limen-staff` as
separate services with `limenctl migrate` as an init container. See
[docs/phases/phase-09a-binary-split.md](docs/phases/phase-09a-binary-split.md)
for the binary split rationale and per-binary scope.

## Configuration

The gateway is configured via a YAML file. Key sections:

| Section | Purpose |
|---------|---------|
| `server` | Host and port bindings |
| `upstreams` | Remote MCP servers to aggregate, with headers and timeouts |
| `codemode` | JS execution timeout and memory limits |
| `auth` | JWT/OAuth validation settings |

See [`docs/configuration.md`](docs/configuration.md) for the full reference, or check [`config.yaml`](config.yaml) for a working example.

## Security

- **Goja sandbox isolation** -- No filesystem or network access. Only explicitly injected tool functions are available to JS code.
- **Execution timeout** -- Configurable JS execution limit (default 30s) prevents infinite loops.
- **Memory limits** -- JS heap capped at configurable size (default 64 MB).
- **Auth middleware** -- JWT validation against a JWKS endpoint protects the gateway endpoint.
- **Remote upstreams only** -- No local MCP server support, eliminating local supply chain risk.

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Goja over V8 | Pure Go, no CGO, single-binary deployment |
| Streamable HTTP | MCP specification standard for remote server communication |
| Tool aggregation at connect time | Enables discovery without per-request round-trips to upstreams |
| Per-tool proxy functions | Natural JS API: `codemode.jira_get_ticket()` instead of string-based dispatch |
| No shell access | Narrower attack surface than CLI wrapper approaches |

## Roadmap

These features are still planned or in progress:

- [x] **JWKS token validation** — Shipped in [Phase 6](docs/phases/phase-06-resource-server.md); `internal/auth/middleware.go` is a full implementation, not a stub
- [ ] **DLP scanning** — Inspect tool responses for sensitive data before forwarding
- [ ] **Tool-level access control** — Per-user/per-role tool permissions (deferred to speculative [Phase 17](docs/phases/phase-17-policy-engine.md))
- [ ] **Observability** — Prometheus metrics export; structured zap logging already shipped in [Phase 8](docs/phases/phase-08-per-tenant-injection.md). See [Phase 16](docs/phases/phase-16-observability-and-active-users.md)
- [ ] **Upstream liveness probing** — Active health-check pings; auto-disable on sustained failure shipped in [Phase 7](docs/phases/phase-07-outbound-upstream.md)
- [ ] **Tool caching** — TTL caching of upstream tool schemas; current [Phase 8](docs/phases/phase-08-per-tenant-injection.md) catalog is DB-backed but not TTL-cached. See [Phase 14](docs/phases/phase-14-upstream-tool-normalization.md)

## What is Limen?

Limen is Latin for *threshold* -- the space between worlds. It sits at the boundary between LLMs and their tools, translating intent into action. A limen is not a wall; it is a doorway, selective about what passes through.

## License

MIT
