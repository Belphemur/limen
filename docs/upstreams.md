# Upstreams

Upstream servers are the MCP-compatible services that Limen aggregates behind a single endpoint. This guide covers upstream configuration, authentication strategies, connection behavior, and troubleshooting.

## Definition

An upstream is any MCP-compatible server that supports the **Streamable HTTP** transport. Limen connects to each provisioned upstream per-tenant, initializes the MCP session, and fetches the full tool list. These tools then become available to clients through Code Mode or direct proxying.

## Configuration

Upstreams are **not** configured in YAML. They are stored per-tenant in the database and managed via:

- `limen create-upstream` CLI command
- The portal UI (Upstreams section)

There is no global upstream list — every upstream is owned by a tenant and carries a **strategy** that drives credential handling.

### Database Fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Unique identifier within the tenant. Used as a prefix for tool names (e.g., `github_search_issues`). |
| `mcp_server_url` | Yes | Full URL of the upstream MCP endpoint. Must use Streamable HTTP transport. |
| `strategy_type` | Yes | One of `none`, `static_header`, or `mcp_spec`. See [Authentication Strategies](#authentication-strategies) below. |

## Authentication Strategies

Each upstream declares a strategy that controls how Limen authenticates outbound requests.

### `none` — No Authentication

For self-hosted or trusted-network servers that require no credentials.

- All users in the tenant get access automatically (`RequiresLink = false`).
- During provisioning, Limen probes `<url>/.well-known/oauth-protected-resource` and **rejects** the upstream if the server advertises a PRM document — that means it wants a Bearer token and the wrong strategy was chosen.
- No headers are attached to outbound requests.

**Use when:** internal MCP servers on a private network, local dev servers.

---

### `static_header` — Static HTTP Header

Attaches a fixed HTTP header to every outbound request. Two sub-modes:

| Sub-mode | Who supplies the secret | `RequiresLink` |
|----------|------------------------|----------------|
| `tenant` | Admin provides a single shared secret at upstream creation | `false` — all tenant users share it automatically |
| `user` | Each user pastes their own key in the portal | `true` — user must connect before tools appear |

The header value is defined as a template with `{value}` as a placeholder, e.g. `Bearer {value}` or `{value}`. Secrets are stored encrypted in the database; the plaintext never touches disk.

**`tenant` mode example** (shared API key):
```
header_name:     Authorization
header_template: Bearer {value}
mode:            tenant
tenant_secret:   <shared-api-key>
```

**`user` mode example** (per-user PAT):
```
header_name:     X-API-Key
header_template: {value}
mode:            user
```
In user mode, each user navigates to the portal's API-key entry page for that upstream before they can call its tools.

**Use when:** SaaS APIs with a shared org token (`tenant`) or per-user personal access tokens (`user`).

---

### `mcp_spec` — MCP-Spec OAuth

For upstreams that implement the MCP OAuth specification. Limen acts as the OAuth client and drives a **code + PKCE (S256)** flow per user. Two client-provisioning sub-modes:

| Sub-mode | When used | How |
|----------|-----------|-----|
| **DCR** (default) | The upstream's AS advertises a `registration_endpoint` | Limen auto-registers itself once per `(tenant, upstream)` via RFC 7591 |
| **Static OAuth client** | No `registration_endpoint` (e.g. GitHub) | Operator provisions a client out-of-band and supplies credentials at upstream creation |

`RequiresLink = true` — each user must complete the OAuth flow in the portal before tools are available.

#### PRM Discovery

Limen discovers the upstream's authorization server in this order:

1. RFC 9728 §3.1 canonical: `<origin>/.well-known/oauth-protected-resource<path>`
2. Legacy path: `<origin><path>/.well-known/oauth-protected-resource`
3. `WWW-Authenticate` header hint: unauthenticated GET to the MCP URL; if the server returns `401 Bearer realm="...", resource_metadata="<URL>"`, fetch that URL.
4. Synthesized PRM from a static `issuer` in the upstream's strategy config — covers servers like `api.githubcopilot.com/mcp/` that don't expose well-known paths.

#### Static OAuth client config fields

| Field | Description |
|-------|-------------|
| `client_id` | OAuth client ID provisioned out-of-band on the AS. Presence of this field skips DCR. |
| `client_secret` | Client secret (empty for public clients). |
| `issuer` | AS issuer URL. Used when AS metadata discovery returns nothing. |
| `authorization_endpoint` | Override when the AS metadata document omits it. |
| `token_endpoint` | Override when the AS metadata document omits it. |
| `scopes` | Additional scopes appended to the authorization request. |

Network-discovered values always take precedence; the static config is a fallback, never an override.

#### Token lifecycle

- `Headers()` returns `Authorization: Bearer <access_token>` and proactively refreshes tokens expiring within `upstream_refresh.proactive_window` (default 60 s).
- The background refresher (`upstream_refresh.refresh_window`, default 5 m) proactively rotates tokens approaching expiry.
- On `invalid_grant` or a 401 that survives a forced refresh, the link is flagged as `needs_relink` and the user must re-authorize in the portal.

**Use when:** Atlassian Rovo, GitHub Copilot, GitLab, or any MCP-spec-compliant OAuth resource server.

## Strategy Selection Guide

| Upstream type | Strategy |
|---------------|----------|
| Trusted/internal, no auth needed | `none` |
| Shared org API key / token | `static_header` (tenant mode) |
| Per-user personal access token | `static_header` (user mode) |
| MCP-spec OAuth resource server with DCR | `mcp_spec` (DCR auto) |
| OAuth server without DCR (GitHub, etc.) | `mcp_spec` (static client) |

## Connection Flow

Limen manages the MCP protocol lifecycle automatically. The sequence for each upstream:

```
1. Initialize     POST to upstream URL with MCP initialization request
   │                 → Negotiates protocol version and capabilities
   ▼
2. ListTools      Requests full tool catalog from upstream
   │                 → Merges tool names with upstream prefix (e.g., "github_")
   ▼
3. Ready          Upstream tools are registered and available for client calls
```

When a client calls a tool through Code Mode:

```
4. CallTool       Limen forwards the tool call to the correct upstream
   │                 → Maps tool name back to upstream, sends args
   ▼
5. Response       Limen returns the upstream's response to the client
```

All of this is handled internally. Clients interact only with Limen's endpoint.

## Protocol

Limen communicates with upstreams using the MCP specification:

- **Protocol version**: `LATEST_PROTOCOL_VERSION` as defined by the MCP spec
- **Transport**: Streamable HTTP -- bidirectional HTTP-based message exchange
- **Message format**: JSON-RPC 2.0 with MCP-specific method names

### Streamable HTTP

Streamable HTTP is the MCP specification's standard transport for remote server communication. It provides:

- Request/response over standard HTTP POST
- Server-sent events (SSE) for server-initiated messages
- Session management via HTTP cookies/headers

Limen uses this exclusively -- **stdio transport (local process execution) is not supported**. This is an intentional security decision to eliminate local supply chain risk.

## Troubleshooting

### Connection Timeout

```
ERROR upstream "github" failed to initialize: context deadline exceeded
```

**Causes:**
- Upstream server is down or unreachable
- Network firewall blocking the connection

**Fixes:**
- Verify the `mcp_server_url` is correct and the server is running
- Test connectivity: `curl -v <url>`

### Authentication Failure (401)

```
ERROR upstream "jira" tool call failed: 401 Unauthorized
```

**Causes (`static_header`):**
- The tenant secret or user API key is expired or wrong
- The `header_template` is missing the `Bearer ` prefix (e.g. template is `{value}` instead of `Bearer {value}`)

**Causes (`mcp_spec`):**
- The user's access token has expired and refresh failed (`needs_relink`)
- The static OAuth client credentials are wrong

**Fixes:**
- For `static_header` tenant mode: update the tenant secret via the portal
- For `static_header` user mode: ask the user to re-enter their key in the portal
- For `mcp_spec`: ask the user to re-link in the portal (OAuth re-authorize flow)

### `none` Strategy Rejected at Provisioning

```
ERROR none: upstream advertises OAuth Protected Resource Metadata; use the mcp_spec strategy instead
```

**Cause:** The upstream at `<url>/.well-known/oauth-protected-resource` returned HTTP 200 — it requires a Bearer token.

**Fix:** Change the strategy to `mcp_spec` (or `static_header` if it uses a simple API key).

### Incompatible Protocol Version

```
ERROR upstream "internal" rejected initialization: unsupported protocol version
```

**Causes:**
- Upstream server is running an older MCP version
- Upstream does not support Streamable HTTP transport (only stdio)

**Fixes:**
- Update the upstream server to a version supporting Streamable HTTP
- Verify the upstream implements the MCP specification correctly
- Confirm the upstream accepts HTTP-based MCP connections (not stdio)

### Tool Not Found

```
ERROR tool "github_search_issues" not found
```

**Causes:**
- Tool name prefix doesn't match the upstream `name`
- Upstream failed to initialize, so its tools were never registered
- Upstream doesn't expose that tool

**Fixes:**
- Confirm the upstream `name` matches the prefix in the tool name
- Look for upstream initialization errors in the gateway logs
- Verify the upstream server actually lists that tool (check upstream docs)
