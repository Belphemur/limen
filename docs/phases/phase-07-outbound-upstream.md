# Phase 7 — Outbound upstream linking (strategies)

**Depends on**: Phase 4 (portal session + tenant resolution); benefits from Phases 1–3 already landed.
**Unblocks**: Phase 8 (per-user upstream injection), Phase 9 (portal UI for upstream connect/disconnect)

## Goal

Replace the current static, single-config-per-process upstream model with a **strategy-driven, per-tenant, per-user** linking system. v1 ships two strategies:

- **`mcp_spec`** — the upstream is itself an MCP-spec-compliant OAuth resource (e.g. Atlassian Rovo at `https://mcp.atlassian.com/v1/mcp/authv2`). Limen acts as the **OAuth client**, performs PRM discovery, dynamic-client-registers itself once per `(tenant, upstream)`, and drives a code+PKCE flow per user.
- **`none`** — the upstream requires no authentication. Useful for self-hosted MCP servers on a trusted network and for development.

The strategy interface is designed so future strategies (`static_header`, `oauth2_app`, `api_token`, `mtls`) drop in without re-architecting.

## Design

### Strategy interface (`internal/upstream/strategy.go`)

```go
type Strategy interface {
    Type() string                            // "mcp_spec", "none", ...
    RequiresLink() bool                      // false for none, true for mcp_spec
    Provision(ctx context.Context, up *storage.Upstream) error
    StartLink(ctx context.Context, up *storage.Upstream, user *storage.User, returnTo string) (redirectURL string, err error)
    FinishLink(ctx context.Context, up *storage.Upstream, user *storage.User, r *http.Request) error
    Headers(ctx context.Context, up *storage.Upstream, link *storage.UpstreamLink) (map[string]string, error)
    Maintain(ctx context.Context, link *storage.UpstreamLink) error
}

type Registry struct { /* ... */ }
func (r *Registry) Register(s Strategy)
func (r *Registry) Get(typ string) (Strategy, error)
```

Method-by-method:

| Method         | When called                                                     | Notes                                                                                                                                                                                                             |
| -------------- | --------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Provision`    | Once after an admin creates an upstream                         | `mcp_spec`: discovers PRM + AS metadata, performs DCR, persists `UpstreamRegistration`. `none`: optional HEAD probe — refuses if upstream advertises PRM (would indicate the operator picked the wrong strategy). |
| `RequiresLink` | Tool listing                                                    | Drives whether `Phase 8` should expose tools to users without a `UpstreamLink`                                                                                                                                    |
| `StartLink`    | User clicks "Connect" in the portal                             | `mcp_spec`: builds an OAuth authorize URL with PKCE + `resource` + state; persists state in DB. `none`: returns an error (never called).                                                                          |
| `FinishLink`   | OAuth callback hit                                              | `mcp_spec`: validates state, exchanges code for tokens, persists `UpstreamLink` with encrypted access/refresh tokens. `none`: not applicable.                                                                     |
| `Headers`      | Per-request, just before forwarding a tool call to the upstream | `mcp_spec`: `Authorization: Bearer <link.access_token>` (refresh inline if expiring within 60 s). `none`: returns empty map.                                                                                      |
| `Maintain`     | Background refresher loop                                       | `mcp_spec`: if `expires_at - now < 5 min`, refresh and persist new tokens. `none`: no-op.                                                                                                                         |

### `mcp_spec` package layout

```
internal/upstream/mcpspec/
├── discovery.go    // fetch PRM + AS metadata; cache per tenant+upstream
├── registrar.go    // DCR against upstream AS; persist UpstreamRegistration
├── link.go         // StartLink (authorize URL), FinishLink (token exchange)
└── headers.go      // Headers() + Maintain() — refresh on expiry
```

Behaviors worth pinning:

- **PKCE S256** mandatory.
- **`resource` parameter** populated with the upstream's resource URI from PRM.
- **State parameter** is an HMAC-signed bundle of `(tenant_id, user_id, upstream_id, nonce, return_to)`. HMAC key is `security.token_encryption_key` (re-use OK — different domain separation string). Stored in a short-lived `OAuthState` table or in a signed cookie; pick whichever is simpler — DB rows are easier to clean up and audit.
- **Token storage**: access + refresh tokens encrypted with AAD `tenant|user|"upstream.<access|refresh>_token"`.
- **Refresh window**: if `expires_at - now < 60 s` at request time, do a synchronous refresh; otherwise rely on the background refresher.

### `none` strategy

Trivial implementation. `Provision` optionally does:

```go
resp, err := http.Get(up.McpServerURL + "/.well-known/oauth-protected-resource")
if err == nil && resp.StatusCode == 200 {
    return errors.New("upstream advertises Protected Resource Metadata; pick 'mcp_spec' instead of 'none'")
}
```

This is a sanity check to catch misconfiguration.

### `internal/upstream/handlers.go` — generic connect routes

Three HTTP handlers, mounted under `/t/{tenant}/upstream/{name}/...`, behind `RequirePortalSession`:

```
POST /t/{tenant}/upstream/{name}/connect      → strategy.StartLink → redirect
GET  /t/{tenant}/upstream/{name}/callback     → strategy.FinishLink → redirect to portal home
POST /t/{tenant}/upstream/{name}/disconnect   → revoke + delete UpstreamLink
```

All three look up `(tenant, upstream)`, fetch the strategy via the registry, and call into it. The portal SPA (Phase 9) calls these via Connect-RPC mutations + browser redirects.

### `internal/upstream/refresher.go` — background maintenance

A single goroutine started in `cmd/gateway/main.go`:

```go
for {
    select {
    case <-time.After(interval):
        ctx := storage.WithSuperuser(context.Background())
        var links []UpstreamLink
        appDB.WithSuperuser(ctx).Where("expires_at IS NOT NULL AND expires_at < ?", time.Now().Add(10*time.Minute)).Find(&links)
        for _, link := range links {
            strat := registry.Get(link.Upstream.StrategyType)
            if err := strat.Maintain(ctx, &link); err != nil {
                log.Warn(...)
            }
        }
    case <-ctx.Done():
        return
    }
}
```

Notes:

- `WithSuperuser` because the loop spans tenants. Audit comment justifying it.
- Interval default: 2 min. Configurable.
- Refresh errors do not delete the link automatically — they log and let the next user request surface the error to the user (who can re-connect).

### Models (already created in Phase 1, recap here)

- `Upstream` — `(tenant_id, name, strategy_type, mcp_server_url)`.
- `UpstreamStrategyConfig` — `(upstream_id, type, config_json)`; encrypted; empty for `mcp_spec` and `none` in v1.
- `UpstreamRegistration` — `(tenant_id, upstream_id, issuer, client_id, client_secret, registration_access_token, registration_client_uri, resource_uri)`; empty for `none`.
- `UpstreamLink` — `(tenant_id, user_id, upstream_id, access_token, refresh_token, expires_at, scopes, resource_uri, extra_json)`.

### State table (optional, choose this path or signed cookie)

```
OAuthState
- ID
- TenantID
- UserID
- UpstreamID
- StateValue (random 32 bytes, base64url)
- ReturnTo
- ExpiresAt (now+10 min)
- Used bool
```

Cleaned up by the janitor. Lookup is constant-time on `StateValue`.

### Tool visibility rule (handed to Phase 8)

Phase 8 will gate per-tool exposure by:

```go
visible := strategy.RequiresLink() == false || userHasLinkFor(upstream, user)
```

`none` upstreams → always visible. `mcp_spec` upstreams → visible only when the user has linked. This is exactly what makes the portal experience feel right: a freshly created tenant sees Atlassian listed as available, prompts the user to connect, and unlocks the tools afterward.

## Deliverables

- New files (under `internal/upstream/`): `strategy.go`, `handlers.go`, `refresher.go`, `mcpspec/{discovery,registrar,link,headers}.go`, `none/none.go`.
- Optional model addition: `OAuthState` (or use a signed-cookie approach).
- Updated `internal/transport/http.go` to mount `/t/{tenant}/upstream/{name}/*` routes.

## Security & operational notes

- **DCR happens once per `(tenant, upstream)`** — guarded by a uniqueness check on `UpstreamRegistration`. Re-running `Provision` after a successful DCR is a no-op (or refreshes registration metadata via RFC 7592 if needed).
- **State must be one-shot** — set `Used=true` on consumption, reject reuse.
- **Scopes are recorded** so the portal can show what's permitted; do not request `offline_access` if the upstream's PRM/AS metadata doesn't advertise refresh tokens.
- **Token storage AAD** binds tokens to the linking user — a stolen token from one row can't be decrypted into another row's slot.
- **Refresh failures are normal events**, not alerts — log at INFO with structured fields (`upstream`, `tenant_id`, `user_id`, `error`).
- **The `none` strategy is a footgun if used on the public internet** — the docs and the strategy's `Provision` PRM sanity-check are the guardrails. Add a `// SAFETY:` comment in the strategy file explaining the intended use cases.

## Verification

- **Unit tests**:
  - State HMAC signing/verification.
  - `mcp_spec` discovery against a stub `httptest.Server` that exposes PRM + AS metadata + DCR endpoint.
  - `mcp_spec` token exchange against a stub token endpoint; verifies persisted tokens are encrypted with correct AAD.
  - `Maintain` does nothing when `expires_at - now > refresh_window`.
  - `none.Provision` rejects an upstream that advertises PRM.
- **Integration**:
  - End-to-end connect flow: admin creates an upstream → user hits `/t/acme/upstream/atlassian/connect` → redirected to upstream's authorize → mock-grants → `/callback` lands tokens → DB shows `UpstreamLink` with encrypted blobs.
  - Refresher loop refreshes a near-expiring token in a fixture DB.

## Risks

- **PRM/DCR variations across vendors** — Atlassian is the reference; some upstreams may deviate (different scope naming, non-standard metadata). The discovery code should be forgiving on optional fields and strict on the mandatory ones.
- **OAuth client secret may not be issued** for public client registrations — the model permits null `client_secret` and the token-exchange path treats public-client registrations with PKCE-only.
- **Long-lived refresher under `WithSuperuser`** is a privileged code path; keep it narrow and audited.

## Checklist

- [ ] `internal/upstream/strategy.go` defines the `Strategy` interface (including `RequiresLink`) and `Registry`
- [ ] Registry populated in `cmd/gateway/main.go` with `mcp_spec` and `none` strategies
- [ ] `internal/upstream/mcpspec/discovery.go` fetches PRM + AS metadata with timeouts and caches the result
- [ ] `internal/upstream/mcpspec/registrar.go` performs DCR once per (tenant, upstream); persists `UpstreamRegistration`
- [ ] `internal/upstream/mcpspec/link.go` builds PKCE+`resource`+state authorize URL and handles token exchange
- [ ] HMAC state is signed with a domain-separated key; persisted (or signed-cookied) with one-shot semantics
- [ ] `internal/upstream/mcpspec/headers.go` injects `Authorization: Bearer ...` and refreshes inline when within 60 s of expiry
- [ ] `internal/upstream/none/none.go` returns empty headers; `Provision` rejects upstreams that advertise PRM
- [ ] Both strategies' `RequiresLink()` returns the right value
- [ ] `internal/upstream/handlers.go` exposes connect / callback / disconnect under `/t/{tenant}/upstream/{name}/*` behind `RequirePortalSession`
- [ ] `internal/upstream/refresher.go` runs a single goroutine under `WithSuperuser(ctx)`, audited with a `// nolint:limen.superuser` comment
- [ ] Refresher interval and refresh window come from config (sensible defaults)
- [ ] Tokens stored encrypted with AAD `tenant|user|"upstream.<kind>_token"`
- [ ] State table (or signed cookie) implemented with one-shot enforcement
- [ ] Unit tests for state signing, discovery, registration, token exchange, refresh, `none.Provision` rejection
- [ ] Integration test for full mcp_spec connect flow against an httptest stub
