# Phase 8 — Per-tenant, per-user upstream injection

**Depends on**: Phases 6, 7
**Unblocks**: Phase 9 (portal listing of available/connected tools)

## Goal

Thread the authenticated `(tenant, user)` ctx — produced by Phase 6's RS middleware — through the gateway and into upstream calls so that every tool invocation uses **that user's** upstream credentials. This is the biggest refactor in the project. Today, [internal/gateway/upstream.go](../../internal/gateway/upstream.go) bakes static headers into the `MCPUpstreamClient` at construction; we move to per-request bearer injection driven by a custom `http.RoundTripper` plus a strategy lookup.

Also: the tool list exposed to a user is filtered by visibility (Phase 7's rule), and the codemode sandbox (`internal/gateway/codemode.go`) consumes the user-scoped tool list.

## Design

### Seams provided by Phase 7

This phase consumes — it does not re-implement — three pieces of machinery that [Phase 7](phase-07-outbound-upstream.md) already shipped:

| Seam                                | Phase 7 surface                                                                                                                                                                                                                                                   | Phase 8 use                                                                                                                                                       |
| ----------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Force-refresh of an `mcp_spec` link | `strategy.HeadersForceRefresh(ctx, upstream, link)` — funnels through Phase 7's `refreshLocked` (single-flight + `SELECT FOR UPDATE SKIP LOCKED` + rotation + `invalid_grant`→`NeedsRelink`)                                                                      | The round-tripper's reactive 401 handler calls this exactly once per request via `AuthProvider.HeadersForceRefresh`.                                              |
| Health-counter mutation             | `upstream.RecordSuccess(ctx, link)` / `upstream.RecordFailure(ctx, link, reason)` — atomic SQL `UPDATE` that resets or bumps `ConsecutiveFailures` / `FirstFailureAt` / `LastFailureAt` / `LastFailureReason` and flips `AutoDisabledAt` when the threshold trips | The round-tripper calls these from the post-response branch in the same goroutine; failure to update is logged, never bubbled to the caller.                      |
| "Re-link required" signal           | `errors.Is(err, upstream.ErrNeedsRelink)` returned by `refreshLocked`                                                                                                                                                                                             | The round-tripper maps a refresh that returns `ErrNeedsRelink` — and a second consecutive 401 after a fresh token — to the structured MCP error documented below. |

Nothing in this phase reaches into the strategy implementations directly; everything goes through the `AuthProvider` (whose `DBAuthProvider` is just a thin facade over `upstream.Registry`) and the three helpers above.

### `AuthProvider` interface and roundtripper

```go
type AuthProvider interface {
    // Headers returns auth headers for the given context.
    // ctx must carry tenant + user; for upstreams with RequiresLink()==false, user may be nil.
    Headers(ctx context.Context, upstream *storage.Upstream) (map[string]string, error)

    // HeadersForceRefresh is like Headers but invalidates any cached token first.
    // Called by the round-tripper after an upstream 401 to drive Phase 7's
    // refreshLocked path; bounded to one call per request.
    HeadersForceRefresh(ctx context.Context, upstream *storage.Upstream) (map[string]string, error)
}
```

The `MCPUpstreamClient` is refactored to:

- Hold one transport per upstream (the upstream URL is fixed).
- The transport is a custom `http.RoundTripper`:

```go
type authInjectingTransport struct {
    base     http.RoundTripper
    upstream *storage.Upstream
    auth     AuthProvider
}

func (t *authInjectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    headers, err := t.auth.Headers(req.Context(), t.upstream)
    if err != nil {
        return nil, err
    }
    req = req.Clone(req.Context())
    for k, v := range headers {
        req.Header.Set(k, v)
    }
    resp, err := t.base.RoundTrip(req)
    if err != nil || resp.StatusCode != http.StatusUnauthorized {
        return resp, err
    }
    // Reactive refresh: drain+close the 401 body, ask the AuthProvider to
    // force a refresh, retry exactly once. A second consecutive 401 surfaces
    // to the caller as a structured "re-link required" MCP error (Phase 7).
    io.Copy(io.Discard, resp.Body)
    _ = resp.Body.Close()
    fresh, err := t.auth.HeadersForceRefresh(req.Context(), t.upstream)
    if err != nil {
        return nil, err
    }
    retry := req.Clone(req.Context())
    for k, v := range fresh {
        retry.Header.Set(k, v)
    }
    return t.base.RoundTrip(retry)
}
```

Because `mcp-go`'s streamable HTTP transport accepts an `http.Client`, we pass it one wired to our transport — bearer injection happens transparently on every request, reading the user from `req.Context()`. The reactive-refresh path is bounded to a single retry to prevent loops; the underlying single-flight + DB lock in Phase 7's `refreshLocked` ensures concurrent requests collapse to one token endpoint hit. The `base` `http.RoundTripper` is the one returned by `internal/resilience.Client("upstream.<name>.calls", cfg)` (Phase 10) so transport-level retries, exponential backoff with jitter, and the per-upstream circuit breaker apply uniformly to every tool call. A `*resilience.BreakerOpenError` returned from `RoundTrip` is mapped to a structured MCP "upstream unavailable" error so the client can back off rather than hammer a dead upstream.

The transport also drives Phase 7's per-user auto-disable mechanism. After the resilience stack returns:

- **Success** (any 2xx): atomically reset `ConsecutiveFailures = 0`, clear `LastFailureAt` / `LastFailureReason`, and clear `AutoDisabledAt` if it was set — in the same DB transaction that records nothing else, so a single `UPDATE` per successful call (cheap and idempotent).
- **Failure** (transport error, persistent 5xx, or `BreakerOpenError`): increment `ConsecutiveFailures`, set `LastFailureAt = now()` and `LastFailureReason` to the matching enum (`tool_call_5xx` / `breaker_open`). When the configured threshold trips, the same `UPDATE` sets `AutoDisabledAt = now()`. The request still returns its error to the caller; the bookkeeping is best-effort and never blocks the response on a DB hiccup.

Before dispatching, the transport short-circuits with the "re-link or re-enable required" structured MCP error if `link.AutoDisabledAt != NULL` or `link.Enabled == false`, so a known-broken link never burns another upstream request.

### `AuthProvider` implementation

Lives in `internal/upstream/authprovider.go`:

```go
type DBAuthProvider struct {
    Store    *storage.Store
    Registry *upstream.Registry
}

func (p *DBAuthProvider) Headers(ctx context.Context, up *storage.Upstream) (map[string]string, error) {
    strat, err := p.Registry.Get(up.StrategyType)
    if err != nil {
        return nil, err
    }
    if !strat.RequiresLink() {
        return strat.Headers(ctx, up, nil)
    }
    user, ok := auth.MCPUserFromContext(ctx)
    if !ok {
        return nil, errors.New("no user in ctx")
    }
    var link storage.UpstreamLink
    tx, commit, err := p.Store.Session(ctx)
    if err != nil {
        return nil, err
    }
    defer commit(&err)
    if err := tx.Where("user_id = ? AND upstream_id = ? AND enabled = TRUE AND auto_disabled_at IS NULL", user.ID, up.ID).First(&link).Error; err != nil {
        return nil, fmt.Errorf("user %d has no enabled link for upstream %q: %w", user.ID, up.Name, err)
    }
    return strat.Headers(ctx, up, &link)
}
```

Cache is intentionally absent at this layer — `strat.Headers` for `mcp_spec` handles short-lived refresh internally, and adding a cache layer over `(tenant, user, upstream)` introduces consistency headaches with revocation.

### Gateway changes (`internal/gateway/gateway.go`)

Replace `AllTools()` with two methods:

```go
// ToolsForUser returns the union of upstream tools the user is authorized to see.
// Rule: include tool if its upstream's strategy.RequiresLink() == false
//   OR the user has an UpstreamLink for that upstream with Enabled=true.
func (g *Gateway) ToolsForUser(ctx context.Context) ([]mcp.Tool, error)

// CallTool routes the request to the right upstream. The injected http.RoundTripper
// reads the ctx and adds the user's credentials at request time.
func (g *Gateway) CallTool(ctx context.Context, upstreamName, toolName string, args map[string]any) (*mcp.CallToolResult, error)
```

Both consume `(tenant, user)` from ctx — set by `RequireMCPAuth` (Phase 6). Helpers: `auth.MCPUserFromContext(ctx)` returns the `*storage.User` pinned by the middleware; `tenancy.TenantFromContext(ctx)` returns the `*storage.Tenant`.

Tool name collisions across upstreams keep the existing convention: prefix tools with the upstream name (`<upstreamName>.<toolName>`).

### Codemode (`internal/gateway/codemode.go`)

The Goja sandbox already exposes `codemode.tools()` and per-tool proxies. Phase 8 changes:

- The handler receives `ctx` (instead of a globally-listed tool set).
- `codemode.tools()` returns the user-scoped subset (`ToolsForUser(ctx)` materialized).
- Each tool proxy in the JS environment is a closure over `(ctx, tool)`; when invoked it calls `gateway.CallTool(ctx, ...)`. Because `ctx` is the per-request ctx, all the per-user injection wiring above applies for free.

The sandbox boundary (no fs, no net, no global escape) is unchanged.

#### Codemode observability (structured logging)

Codemode is the highest-risk surface in the gateway: arbitrary tenant-supplied JavaScript composes upstream tool calls under a user's credentials. v1 ships **structured zap logging** for every code-mode lifecycle event so operators (and, post-[Phase 12](phase-12-staff-backoffice.md), the audit pipeline) can reconstruct exactly what ran. Persisted `audit_events` rows for the same lifecycle land in Phase 12 — the row shape, AAD construction, and encryption rules live in [docs/audit.md](../../docs/audit.md).

A single `codemode_invocation_id` (`cmi_<ulid>`) is generated at handler entry, propagated on ctx, and stamped on every log line below — it is the join key between zap logs (Phase 8) and `audit_events` rows (Phase 12).

Mandatory log lines, all at `Info` (errors at `Error`), all carrying `tenant_id`, `user_id`, `request_id`, `codemode_invocation_id`:

| Event                           | Fields (in addition to the base set)                                                                                                              |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `codemode.invocation.started`   | `script_sha256`, `script_bytes`, `tool_count_visible` (size of `ToolsForUser(ctx)`)                                                               |
| `codemode.tool.called`          | `upstream`, `tool`, `args_sha256`, `args_bytes`, `call_seq` (monotonic within the invocation)                                                     |
| `codemode.tool.completed`       | `upstream`, `tool`, `call_seq`, `result_bytes`, `duration_ms`, `outcome` (`ok` \| `upstream_error` \| `denied_no_link` \| `denied_auto_disabled`) |
| `codemode.tool.error`           | `upstream`, `tool`, `call_seq`, `error_kind`, `error_message` (redacted — see below)                                                              |
| `codemode.invocation.completed` | `tool_calls_total`, `duration_ms`, `outcome` (`ok` \| `script_error` \| `timeout` \| `out_of_memory`), `result_bytes`                             |

Redaction rules — enforced by a small helper in `internal/gateway/codemode.go`, not left to call sites:

- **Zap logs never carry raw content.** Raw script source, raw tool arguments, raw tool results, and upstream auth headers go in via SHA-256 digests + byte counts only.
- The **raw script and the raw response** for the two MCP tools `codemode_search` and `codemode_execute` are persisted — **encrypted** — on the `audit_events` row defined in [docs/audit.md](../../docs/audit.md) (writer + table ship in [Phase 12](phase-12-staff-backoffice.md)). Until Phase 12 ships the writer, they exist only in memory on the request goroutine; they are **not** written to zap logs or any other sink.
- Error messages are passed through `redactSecrets(err)` which strips anything matching `Authorization:` / `Bearer ` / `Set-Cookie:` and replaces values for known secret keys (`access_token`, `refresh_token`, `api_key`, `client_secret`).
- `args_sha256` / `result_bytes` give enough fingerprint to diff two invocations without storing PII in the log stream — the encrypted audit row is the source of truth when forensic detail is needed.

Resource ceilings — enforced by the sandbox, **logged** at the boundary:

- Script wall-clock timeout (config: `codemode.script_timeout`, default 10 s) — hitting it emits `codemode.invocation.completed` with `outcome=timeout`.
- Per-invocation tool-call cap (config: `codemode.max_tool_calls`, default 50) — exceeding emits `codemode.tool.error` with `error_kind=quota_exceeded` and then a `script_error` invocation outcome.
- Goja interrupt on ctx cancellation (client disconnect, server shutdown) — logged as `outcome=cancelled`.

### Transport (`internal/transport/http.go`)

Routes after Phase 8:

```
/t/{tenant}/                                      → RequireTenant
  ├─ /portal/...                                  → portal session + SPA (Phase 9)
  ├─ /oauth/...                                   → AS endpoints (Phase 5)
  ├─ /mcp/.well-known/oauth-protected-resource    → public (Phase 6)
  ├─ /mcp                                          → RequireMCPAuth → MCP handler
  ├─ /mcp/                                         → RequireMCPAuth → MCP handler
  └─ /upstream/{name}/{connect,callback,disconnect}/ → RequirePortalSession (Phase 7)
```

The old top-level `/mcp` route is removed. Deployments that want a default-tenant convenience redirect (`/mcp` → `/t/<default-tenant-public-id>/mcp`) can wire one at the operator level, but it is not implemented by default.

### `mcp-go` server per tenant?

No. The MCP `server.SSEServer` is still a single instance — the per-tenant filtering happens inside the gateway's `ToolsForUser` and `CallTool`. The server's session state is keyed by `(tenant_id, user_id, mcp_session_id)` to prevent cross-user MCP session continuation (already noted in Phase 6).

### `UpstreamManager`

A small index built at startup (and on `Upstream`/`UpstreamRegistration` changes — for v1, a simple "reload on change" via signal or admin RPC suffices):

```go
type UpstreamManager struct {
    // map[tenantID]map[upstreamName]*Bundle — tenantID is storage.Tenant.ID (BIGINT).
    bundles map[int64]map[string]*Bundle
}

type Bundle struct {
    Upstream *storage.Upstream
    Client   *MCPUpstreamClient   // transport wired with authInjectingTransport
}
```

`Gateway.ToolsForUser` and `Gateway.CallTool` walk through `bundles[tenantID]` to enumerate / look up.

## Deliverables

- Rewritten files:
  - `internal/gateway/upstream.go`
  - `internal/gateway/gateway.go`
  - `internal/gateway/codemode.go` (mostly: read tool list from ctx-aware source)
  - `internal/transport/http.go`
- New file: `internal/upstream/authprovider.go`.

## Security & operational notes

- **No tokens in logs**: the transport must not log request headers. `mcp-go`'s logger may log request URLs; verify it doesn't dump headers.
- **`http.Request.Clone` per round-trip** to avoid mutating a shared request object.
- **Timeouts**: every upstream client carries an `http.Client.Timeout` from config (default 30 s). Per-tool overrides aren't in scope.
- **Error propagation**: when `Headers` fails (no link), surface as a structured MCP error to the caller — _not_ a 500. The client should be able to ask the user to (re-)connect.
- **`MCPSessionID` binding**: when the MCP server tracks a session, the session record must include `tenant_id + user_id`. A token swap _should_ mid-session be allowed only if `(tenant, user)` matches.

## Verification

- **Unit tests**:
  - `authInjectingTransport.RoundTrip` adds the right header given a ctx; surfaces errors from `Headers`; clones the request.
  - `ToolsForUser` returns the correct union for users with / without links.
  - `CallTool` routes to the correct upstream and bubbles upstream errors.
- **Integration tests**:
  - Two tenants, two users each. Bound tokens for user A1 against tenant A, user B1 against tenant B. A call by A1 to a tenant-A upstream succeeds with A1's bearer; B1 hitting the same upstream name fails (it's a different tenant's upstream).
  - One `none` upstream + one `mcp_spec` upstream. Brand-new user sees `none` tools but not `mcp_spec` ones until they connect.
  - User disconnects → tool disappears from `ToolsForUser` and `CallTool` returns the "no link" structured error.
  - User toggles `Enabled=false` on an existing link → tool disappears from `ToolsForUser` and `CallTool` returns the same structured error; toggling back to `Enabled=true` restores visibility without re-running the auth flow.
  - Sustained upstream failures (5xx loop or breaker-open) → link's `ConsecutiveFailures` grows, `AutoDisabledAt` flips at the threshold, tool disappears from `ToolsForUser`; portal Re-enable clears it and the next 2xx confirms health.

## Risks

- **`mcp-go` transport assumptions**: the streamable HTTP transport may not currently surface ctx into `RoundTrip` cleanly. Verify with a small spike before committing to this design. If ctx isn't preserved end-to-end, fall back to a per-call `http.Client` construction with ctx encoded into a wrapper transport via `WithValue` — slower but correct.
- **Session collisions in MCP server**: `mcp-go` keys sessions by `Mcp-Session-Id`. We must namespace by tenant in the key.
- **Hot-reload of upstreams** is not implemented in v1; restart picks up new upstreams. Document this.

## Checklist

- [ ] `internal/upstream/authprovider.go` defines `AuthProvider` (with `Headers` + `HeadersForceRefresh`) and `DBAuthProvider`
- [ ] `internal/gateway/upstream.go` uses an `http.RoundTripper` that reads ctx and calls `AuthProvider.Headers`
- [ ] Round-trip clones the request before mutating headers
- [ ] Round-tripper retries exactly once on upstream `401`, calling `HeadersForceRefresh` to drive Phase 7's refresh path; a second consecutive `401` surfaces as a structured "re-link required" MCP error
- [ ] `UpstreamManager` builds an index keyed by tenant ID at startup
- [ ] `Gateway.ToolsForUser(ctx)` filters by `strategy.RequiresLink()==false` ∨ (user-has-link ∧ `link.Enabled` ∧ `link.AutoDisabledAt IS NULL`)
- [ ] `Gateway.CallTool(ctx, upstreamName, toolName, args)` looks up upstream within tenant, invokes through the per-request transport; rejects calls when the matching link is disabled or auto-disabled with the same structured error as missing-link
- [ ] Round-tripper updates `UpstreamLink` health columns after each call: 2xx resets `ConsecutiveFailures` / clears `AutoDisabledAt`; persistent 5xx / `BreakerOpenError` increments the counter and flips `AutoDisabledAt` when the threshold trips
- [ ] Tool names continue to be prefixed by upstream name to avoid collisions
- [ ] Missing-link condition surfaces as a structured MCP error (not 500)
- [ ] `internal/gateway/codemode.go` exposes only user-scoped tools to the sandbox; per-tool proxies call into `Gateway.CallTool(ctx, ...)`
- [ ] Codemode emits the structured `codemode.invocation.started` / `codemode.tool.called` / `codemode.tool.completed` / `codemode.tool.error` / `codemode.invocation.completed` zap log lines documented above, every line carrying `codemode_invocation_id` (`cmi_<ulid>`)
- [ ] `redactSecrets` helper applied at every codemode log call site; raw scripts, raw tool args, raw tool results, and auth headers never appear in logs
- [ ] Config keys `codemode.script_timeout` and `codemode.max_tool_calls` shipped with the defaults above; both enforced by the sandbox and logged when exceeded
- [ ] `internal/transport/http.go` mounts MCP routes only under `/t/{tenant}/mcp` behind `RequireMCPAuth`
- [ ] Top-level `/mcp` route removed
- [ ] MCP server session state keyed by `(tenant_id, user_id, mcp_session_id)`
- [ ] No request headers logged; verified by inspection of `mcp-go` logger settings
- [ ] Unit tests for transport, tool filtering, routing
- [ ] Integration tests for multi-tenant isolation and link-required visibility
- [ ] Integration test: full `mcp_spec` connect flow against an httptest stub — admin creates an upstream, user hits `/connect`, mock authorize + token endpoints, `/callback` lands tokens, a subsequent tool call goes out with the injected `Authorization: Bearer ...` header _(moved from [Phase 7](phase-07-outbound-upstream.md) — needs this phase's round-tripper to assert end-to-end Headers injection on the upstream call)_
- [ ] Integration test: reactive refresh on `401` — stub upstream returns 401 once, then 200; the round-tripper transparently calls `HeadersForceRefresh` and retries the tool call exactly once, which succeeds _(moved from [Phase 7](phase-07-outbound-upstream.md) — the retry loop lives in this phase's round-tripper)_
- [ ] Integration test (server-side half): sustained upstream 5xx through the round-tripper → `RecordFailure` trips `AutoDisabledAt` at the configured threshold → tool listing for that user hides the upstream's tools → subsequent `CallTool` returns the structured "re-link or re-enable required" error _(moved from [Phase 7](phase-07-outbound-upstream.md) — failure accounting happens in this phase's transport; the portal-side Re-enable round-trip is covered by [Phase 9](phase-09-portal-spa.md))_
