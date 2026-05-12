# Phase 8 — Per-tenant, per-user upstream injection

**Depends on**: Phases 6, 7
**Unblocks**: Phase 9 (portal listing of available/connected tools)

## Goal

Thread the authenticated `(tenant, user)` ctx — produced by Phase 6's RS middleware — through the gateway and into upstream calls so that every tool invocation uses **that user's** upstream credentials. This is the biggest refactor in the project. Today, [internal/gateway/upstream.go](../../internal/gateway/upstream.go) bakes static headers into the `MCPUpstreamClient` at construction; we move to per-request bearer injection driven by a custom `http.RoundTripper` plus a strategy lookup.

Also: the tool list exposed to a user is filtered by visibility (Phase 7's rule), and the codemode sandbox (`internal/gateway/codemode.go`) consumes the user-scoped tool list.

## Design

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

### `AuthProvider` implementation

Lives in `internal/upstream/authprovider.go`:

```go
type DBAuthProvider struct {
    Store    *storage.Store
    Registry *upstream.Registry
}

func (p *DBAuthProvider) Headers(ctx, up) (map[string]string, error) {
    strat, err := p.Registry.Get(up.StrategyType)
    if err != nil {
        return nil, err
    }
    if !strat.RequiresLink() {
        return strat.Headers(ctx, up, nil)
    }
    user, ok := authctx.UserFromContext(ctx)
    if !ok {
        return nil, errors.New("no user in ctx")
    }
    var link storage.UpstreamLink
    db, commit, err := p.Store.Session(ctx)
    defer commit(&err)
    if err := db.Where("user_id = ? AND upstream_id = ? AND enabled = true", user.ID, up.ID).First(&link).Error; err != nil {
        return nil, fmt.Errorf("user %q has no enabled link for upstream %q", user.ID, up.Name)
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

Both consume `(tenant, user)` from ctx — set by `RequireMCPAuth` (Phase 6).

Tool name collisions across upstreams keep the existing convention: prefix tools with the upstream name (`<upstreamName>.<toolName>`).

### Codemode (`internal/gateway/codemode.go`)

The Goja sandbox already exposes `codemode.tools()` and per-tool proxies. Phase 8 changes:

- The handler receives `ctx` (instead of a globally-listed tool set).
- `codemode.tools()` returns the user-scoped subset (`ToolsForUser(ctx)` materialized).
- Each tool proxy in the JS environment is a closure over `(ctx, tool)`; when invoked it calls `gateway.CallTool(ctx, ...)`. Because `ctx` is the per-request ctx, all the per-user injection wiring above applies for free.

The sandbox boundary (no fs, no net, no global escape) is unchanged.

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

The old top-level `/mcp` route is removed. Deployments that want a default-tenant convenience redirect (`/mcp` → `/t/<default-slug>/mcp`) can wire one at the operator level, but it is not implemented by default.

### `mcp-go` server per tenant?

No. The MCP `server.SSEServer` is still a single instance — the per-tenant filtering happens inside the gateway's `ToolsForUser` and `CallTool`. The server's session state is keyed by `(tenant_id, user_id, mcp_session_id)` to prevent cross-user MCP session continuation (already noted in Phase 6).

### `UpstreamManager`

A small index built at startup (and on `Upstream`/`UpstreamRegistration` changes — for v1, a simple "reload on change" via signal or admin RPC suffices):

```go
type UpstreamManager struct {
    // map[tenantID]map[upstreamName]*upstream.Bundle
    bundles map[uuid.UUID]map[string]*Bundle
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
- [ ] `Gateway.ToolsForUser(ctx)` filters by `strategy.RequiresLink()==false` ∨ (user-has-link ∧ `link.Enabled`)
- [ ] `Gateway.CallTool(ctx, upstreamName, toolName, args)` looks up upstream within tenant, invokes through the per-request transport; rejects calls when the matching link is disabled with the same structured error as missing-link
- [ ] Tool names continue to be prefixed by upstream name to avoid collisions
- [ ] Missing-link condition surfaces as a structured MCP error (not 500)
- [ ] `internal/gateway/codemode.go` exposes only user-scoped tools to the sandbox; per-tool proxies call into `Gateway.CallTool(ctx, ...)`
- [ ] `internal/transport/http.go` mounts MCP routes only under `/t/{tenant}/mcp` behind `RequireMCPAuth`
- [ ] Top-level `/mcp` route removed
- [ ] MCP server session state keyed by `(tenant_id, user_id, mcp_session_id)`
- [ ] No request headers logged; verified by inspection of `mcp-go` logger settings
- [ ] Unit tests for transport, tool filtering, routing
- [ ] Integration tests for multi-tenant isolation and link-required visibility
