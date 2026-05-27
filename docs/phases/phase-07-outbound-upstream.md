---
phase: "7"
title: "Outbound upstream linking (strategies)"
status: completed
progress: 100
depends_on: ["4"]
updated: "2025-12-01"
---

# Phase 7 — Outbound upstream linking (strategies)

**Depends on**: Phase 4 (portal session + tenant resolution); benefits from Phases 1–3 already landed.
**Unblocks**: Phase 8 (per-user upstream injection), Phase 9b (portal UI for upstream connect/disconnect)

## Goal

Replace the current static, single-config-per-process upstream model with a **strategy-driven, per-tenant, per-user** linking system. v1 ships three strategies:

- **`mcp_spec`** — the upstream is itself an MCP-spec-compliant OAuth resource (e.g. Atlassian Rovo at `https://mcp.atlassian.com/v1/mcp/authv2`). Limen acts as the **OAuth client** and drives a code+PKCE flow per user. The strategy supports two client-provisioning modes, picked automatically per upstream:
  - **DCR (default)** — when the upstream's authorization server advertises a `registration_endpoint`, Limen dynamic-client-registers itself once per `(tenant, upstream)` via RFC 7591.
  - **Static OAuth client** — when the AS does **not** support DCR (e.g. `https://github.com/login/oauth`), the operator provisions a client out-of-band and passes the credentials at upstream creation time. Limen stores them encrypted in `UpstreamStrategyConfig.ConfigJSON` and uses them as if DCR had run. The same payload can carry `issuer`, `authorization_endpoint`, `token_endpoint`, and `scopes` overrides for servers that ship no AS metadata document at all.
- **`static_header`** — the upstream authenticates via a static HTTP header (typically `Authorization: Bearer <token>` or `X-API-Key: <key>`). Two sub-modes, chosen by the admin at upstream creation:
  - **`tenant`**: the admin supplies a single shared secret encrypted on the `UpstreamStrategyConfig`. All users of the tenant share it. `RequiresLink()` returns `false` (the tools become visible to everyone in the tenant automatically).
  - **`user`**: each user must paste their own API key in the portal before the upstream is usable. The secret lives encrypted on the `UpstreamLink`. `RequiresLink()` returns `true`.
- **`none`** — the upstream requires no authentication. Useful for self-hosted MCP servers on a trusted network and for development.

The strategy interface is designed so future strategies (`oauth2_app`, `api_token`, `mtls`) drop in without re-architecting.

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

| Method         | When called                                                     | Notes                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| -------------- | --------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Provision`    | Once after an admin creates an upstream                         | `mcp_spec`: discovers PRM + AS metadata; if the AS advertises `registration_endpoint` performs DCR, otherwise reads the static OAuth client from `UpstreamStrategyConfig` (errors with an actionable message if neither is available). Persists `UpstreamRegistration` either way. `static_header`: validates config (header name, template, mode); persists tenant secret encrypted if mode==`tenant`. `none`: optional HEAD probe — refuses if upstream advertises PRM (would indicate the operator picked the wrong strategy). |
| `RequiresLink` | Tool listing                                                    | Drives whether `Phase 8` should expose tools to users without a `UpstreamLink`. `false` for `none` and `static_header` (tenant mode); `true` for `mcp_spec` and `static_header` (user mode).                                                                                                                                                                                                                                                                                                                                      |
| `StartLink`    | User clicks "Connect" in the portal                             | `mcp_spec`: builds an OAuth authorize URL with PKCE + `resource` + state. `static_header` (user mode): returns a portal SPA URL where the user pastes their key (no external redirect). `none` / `static_header` (tenant mode): returns an error (never called).                                                                                                                                                                                                                                                                  |
| `FinishLink`   | OAuth callback hit                                              | `mcp_spec`: validates state, exchanges code for tokens, persists `UpstreamLink`. `static_header` (user mode): wired through the portal `SubmitUpstreamAPIKey` RPC, not an HTTP callback. `none`: not applicable.                                                                                                                                                                                                                                                                                                                  |
| `Headers`      | Per-request, just before forwarding a tool call to the upstream | `mcp_spec`: `Authorization: Bearer <link.access_token>` (refresh inline if expiring within 60 s). `static_header`: renders `header_template` with the tenant secret or user-supplied key. `none`: returns empty map.                                                                                                                                                                                                                                                                                                              |
| `Maintain`     | Background refresher loop                                       | `mcp_spec`: if `expires_at - now < 5 min`, refresh and persist new tokens. `static_header` / `none`: no-op.                                                                                                                                                                                                                                                                                                                                                                                                                       |

### `mcp_spec` package layout

The strategy lives under `internal/upstream/mcpspec/`, split by concern:

```
internal/upstream/mcpspec/
├── strategy.go   // Strategy struct, Options, constructor, Type/RequiresLink
├── discovery.go  // PRM + AS metadata discovery; RFC 9728 canonical + legacy
│                 // well-known paths; WWW-Authenticate fallback; config overlay
├── config.go     // static-client Config (JSON in UpstreamStrategyConfig);
│                 // EncodeConfig + loadConfig
├── provision.go  // Provision() — DCR or static-client fallback; loadRegistration
├── link.go       // StartLink (authorize URL), FinishLink (token exchange), PKCE
└── refresh.go    // Headers, ensureFresh, refreshLink, Maintain, tokenRequest
```

#### PRM discovery (RFC 9728)

The strategy tries each well-known location, in order, before giving up:

1. RFC 9728 §3.1 canonical: `<origin>/.well-known/oauth-protected-resource<path>`
2. Legacy / pre-RFC: `<origin><path>/.well-known/oauth-protected-resource`
3. `WWW-Authenticate` hint: an unauthenticated GET against the MCP URL itself; if the server responds with `401 Bearer realm="...", resource_metadata="<URL>"`, fetch that URL.

If none of those return a PRM document **and** the operator has provisioned a static `Config` with an `issuer`, the strategy synthesizes a PRM from the upstream URL + configured issuer so the rest of the flow can proceed. This is what makes servers like `api.githubcopilot.com/mcp/` work — they don't expose the well-known paths.

#### AS metadata overlay

`discover()` fetches the OAuth AS metadata document (trying the four well-known shapes — RFC 8414 and OIDC, with and without path-suffix). When any of `issuer`, `authorization_endpoint`, or `token_endpoint` are missing from the network response — or the document is missing entirely — the static `Config` fills them in. Network values always win when present; the config is a fallback, never an override.

#### Static OAuth client `Config`

Stored encrypted as JSON in `UpstreamStrategyConfig.ConfigJSON` (AAD `tenant|""|"upstream.mcpspec.strategy_config"`):

```jsonc
{
  "issuer": "https://github.com/login/oauth", // optional override
  "authorization_endpoint": "https://github.com/login/oauth/authorize", // optional override
  "token_endpoint": "https://github.com/login/oauth/access_token", // optional override
  "client_id": "Iv1.xxxxxxxxxxxxxxxx", // required for static mode
  "client_secret": "...", // required for confidential clients
  "scopes": ["read:user", "repo"], // appended to the authorize URL
}
```

The presence of `client_id` is the signal that `Provision()` should skip DCR.

Behaviors worth pinning:

- **PKCE S256** mandatory.
- **`resource` parameter** populated with the upstream's resource URI from PRM.
- **State parameter** is an HMAC-signed bundle of `(tenant_id, user_id, upstream_id, nonce, return_to)`. HMAC key is derived from `security.token_encryption_key` via HKDF with the domain tag `"upstream.oauth.state"` (so it cannot be confused with the Phase 4 portal-state MAC). State is **persisted in Valkey** (the same instance Zitadel uses for its cache; Limen owns its own logical keyspace under `limen:upstream:state:<state_value>`) with a 10-minute TTL. Valkey handles expiry, so no janitor is needed; one-shot enforcement uses `GETDEL` so a replay of the same `state` value finds nothing. The Valkey payload carries the signed envelope plus the PKCE verifier, encrypted with AAD `tenant|user|"upstream.pkce"` — Valkey never sees a plaintext verifier.
- **Token storage**: access + refresh tokens encrypted with AAD `tenant|user|"upstream.<access|refresh>_token"`.
- **Refresh window**: if `expires_at - now < 60 s` at request time, do a synchronous refresh; otherwise rely on the background refresher.
- **Refresh-token rotation**: many providers (including Atlassian and Zitadel) issue a new refresh token on every refresh. The refresh path **must** persist whichever of `access_token`, `refresh_token`, `expires_at`, and `scopes` the response returned; never assume the old refresh token still works after a successful exchange.
- **Single-flight coordination**: concurrent tool calls for the same `(tenant, user, upstream)` must not stampede the token endpoint. The `mcp_spec` headers path wraps the refresh exchange in a per-link single-flight (`golang.org/x/sync/singleflight`, key = `link.PublicID`). The first caller does the network round-trip and DB write; followers block on the same call and read the updated row. Combined with `SELECT ... FOR UPDATE SKIP LOCKED` on the link row, this also coordinates across multiple Limen processes — a peer that already holds the lock wins, others re-read after the lock releases.
- **Reactive refresh on `401`**: even with proactive refresh, an upstream may invalidate a token early (revocation, rotation). The custom `http.RoundTripper` (Phase 8) detects upstream `401`/`invalid_token`, triggers `strategy.Refresh(ctx, link)` through the same single-flight, and retries the request exactly once with the new token. A second consecutive `401` is surfaced to the caller as a structured "re-link required" MCP error.
- **Refresh failure semantics**: a refresh that fails with `invalid_grant` (the upstream considers the refresh token dead) marks the link as `needs_relink=true` (a thin column on `UpstreamLink`, default `false`); the portal shows a "Reconnect" CTA on that row. Transient failures (5xx / network) are logged and retried by the background refresher on the next tick.
- **Resilience**: the HTTP client used for the token endpoint, discovery, and DCR is built through `internal/resilience.Client("upstream.<name>.<purpose>", cfg)` (Phase 10), so retries with exponential backoff + jitter and a per-upstream circuit breaker apply automatically. A `*resilience.BreakerOpenError` from the refresh path is surfaced as a transient error — it does **not** set `needs_relink`; the link recovers on its own once the breaker half-opens.

### `static_header` strategy

Package layout:

```
internal/upstream/statichdr/
├── config.go     // marshalling of UpstreamStrategyConfig: header name, mode (tenant|user), template
├── link.go       // StartLink/FinishLink for user-mode: not OAuth — render a form route, accept a POST with the key, persist UpstreamLink
└── headers.go    // Headers(): reads the tenant secret or the user link, formats the header per template
```

Behaviors:

- **Config schema** (stored in `UpstreamStrategyConfig.ConfigJSON`, encrypted with AAD `tenant|""|"upstream.strategy_config"`):

  ```json
  {
    "header_name": "Authorization",
    "header_template": "Bearer {value}",
    "mode": "user", // "tenant" or "user"
    "tenant_secret": "..." // populated only when mode == "tenant"
  }
  ```

  `{value}` is the only substitution. `header_name` and `header_template` are admin-controlled at upstream creation; the user-facing form only takes the raw secret.

- **`StartLink` (user mode)** does **not** redirect to a third-party authorize endpoint. It returns a relative URL into the portal SPA (e.g. `/portal/upstreams/<public_id>/api-key`) where the SPA renders the form. The actual submission goes through a dedicated Connect-RPC method on `PortalService` (`SubmitUpstreamAPIKey`, see Phase 9b), **not** through the generic `/callback` route. The strategy still implements `FinishLink` for shape uniformity, but it is not wired to an HTTP callback.

- **`Provision` (tenant mode)** validates that `tenant_secret` is non-empty and that `header_template` parses; performs no network call.

- **Per-user secret storage**: stored in `UpstreamLink.ExtraJSON` (already an encrypted `SecretField`) under a dedicated key so we don't grow the schema. AAD remains `tenant|user|"upstream.extra"`.

- **Rotation**: `static_header` exposes a portal action to overwrite the stored key. Old key is replaced atomically.

- **No background `Maintain`** — static secrets do not expire from Limen's perspective. The refresher loop skips `static_header` links.

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

Three HTTP handlers, mounted under `/t/{tenant}/upstream/{name}/...`, behind `RequirePortalSession`. For `static_header` user-mode upstreams, `connect` redirects to the SPA's API-key form rather than an external authorize URL, and the `callback` handler is unused (the SPA POSTs the key through Connect-RPC):

```
POST /t/{tenant}/upstream/{name}/connect      → strategy.StartLink → redirect
GET  /t/{tenant}/upstream/{name}/callback     → strategy.FinishLink → redirect to portal home
POST /t/{tenant}/upstream/{name}/disconnect   → revoke + delete UpstreamLink
```

All three look up `(tenant, upstream)`, fetch the strategy via the registry, and call into it. The portal SPA (Phase 9b) calls these via Connect-RPC mutations + browser redirects.

### Token-refresh control flow (`mcp_spec`)

Three code paths can trigger a refresh — they all funnel through one function so the locking, persistence, and rotation rules live in exactly one place.

```go
// internal/upstream/mcpspec/headers.go
func (s *Strategy) refreshLocked(ctx context.Context, link *storage.UpstreamLink) error {
    // 1. SELECT ... FOR UPDATE SKIP LOCKED on the link row inside a tx.
    // 2. Re-read expires_at: if another process already refreshed within the
    //    last few seconds, return without calling the token endpoint.
    // 3. POST /token with grant_type=refresh_token, refresh_token=<decrypted>.
    // 4. On success: persist new access_token, refresh_token (if returned),
    //    expires_at, scopes (if returned); clear needs_relink.
    // 5. On invalid_grant: set needs_relink=true, commit, return a sentinel
    //    error (errors.Is(err, ErrNeedsRelink)).
    // 6. On transient error: rollback, return the error.
}

var group singleflight.Group

func (s *Strategy) ensureFresh(ctx, link) error {
    if time.Until(link.ExpiresAt) > 60*time.Second {
        return nil
    }
    _, err, _ := group.Do(link.PublicID, func() (any, error) {
        return nil, s.refreshLocked(ctx, link)
    })
    return err
}
```

Entry points:

| Path                    | Calls                                                                        | Notes                                                                                       |
| ----------------------- | ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `Headers()`             | `ensureFresh` if near-expiry                                                 | Proactive; happens before the upstream request is dispatched.                               |
| `RoundTripper` on `401` | `ensureFresh` then retry once                                                | Reactive; handles unexpected invalidation. Bounded to one retry per request to avoid loops. |
| `Maintain()`            | `refreshLocked` directly via `ensureFresh` over the background-loop link set | Same lock, same persistence path — the background loop is just another caller.              |

All three share the same single-flight key and DB lock, so a burst of tool calls during the refresh window collapses to one token endpoint hit.

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
- Refresh errors do not delete the link automatically — they log and let the next user request surface the error to the user (who can re-connect). A `needs_relink=true` link is skipped by the background loop (no point re-trying a dead refresh token) until the user runs `StartLink` again.

### Models (already created in Phase 1, recap here)

- `Upstream` — `(tenant_id, name, strategy_type, mcp_server_url)`.
- `UpstreamStrategyConfig` — `(upstream_id, type, config_json)`; encrypted. Populated for `static_header` (header name, template, mode, optional tenant secret) and for `mcp_spec` upstreams whose AS does not support DCR (static client_id / client_secret + optional issuer / endpoint / scopes overrides). Empty for `none` and for `mcp_spec` upstreams that use DCR.
- `UpstreamRegistration` — `(tenant_id, upstream_id, issuer, client_id, client_secret, registration_access_token, registration_client_uri, resource_uri)`. Populated for every `mcp_spec` upstream, regardless of whether the credentials came from DCR or from a static `Config`; empty for `none` and `static_header`.
- `UpstreamLink` — `(tenant_id, user_id, upstream_id, access_token, refresh_token, expires_at, scopes, resource_uri, extra_json, enabled, needs_relink, consecutive_failures, last_failure_at, last_failure_reason, auto_disabled_at)`.
  - **`Enabled bool` (new in this phase)** — defaults to `true` on create. The portal exposes a toggle so a user can mute an upstream without losing their stored credentials. Phase 8's tool-visibility filter treats `Enabled=false` the same as "no link".
  - **`NeedsRelink bool` (new in this phase)** — defaults to `false`. Set by `refreshLocked` when the upstream returns `invalid_grant`; cleared on the next successful refresh or when the user re-runs `StartLink`. The portal renders a "Reconnect" CTA on rows where this is true.
  - **Health-tracking columns (new in this phase)** — `ConsecutiveFailures int` (default `0`), `LastFailureAt *time.Time`, `LastFailureReason string` (short enum: `refresh_transient`, `breaker_open`, `tool_call_5xx`, `invalid_grant`), and `AutoDisabledAt *time.Time`. Every successful tool call or refresh resets `ConsecutiveFailures` to `0`, clears `LastFailureAt` / `LastFailureReason`, and clears `AutoDisabledAt`. Sustained failure flips `AutoDisabledAt = now()` (see below).
  - For `static_header` user-mode, the user's API key lives encrypted in `ExtraJSON` under the key `static_header.secret`. `AccessToken`/`RefreshToken`/`ExpiresAt` stay empty.

### Per-user auto-disable on sustained failure

The goal is to stop hammering an upstream that is reliably failing for a specific user, without losing the user's credentials. Tool listing must also stop advertising tools that will only fail. The rules:

- **What counts as a failure**: a refresh that hits `invalid_grant` (already covered by `NeedsRelink`); a refresh that hits transport / 5xx repeatedly through the resilience stack until the breaker opens; a tool call that returns 5xx or transport error after the resilience retries; a `BreakerOpenError` surfaced from either path.
- **What counts as a success**: any tool call that returns 2xx, or any successful refresh exchange.
- **Where the counter is updated**: Phase 8's `authInjectingTransport` (tool calls) and Phase 7's `refreshLocked` (refresh path) bump or reset `ConsecutiveFailures` in the same DB transaction that persists the call outcome, so it never drifts from reality.
- **Auto-disable thresholds** (config-driven, with shipped defaults):
  - `≥5 consecutive failures` **and** `LastFailureAt - FirstFailureAt ≥ 15 min` (where `FirstFailureAt` is a dedicated column set on the 0→1 transition and cleared on any success), **or**
  - `NeedsRelink=true` continuously for `≥ 24 h` without a successful re-link.

  On trip: set `AutoDisabledAt = now()`, leave `Enabled` as-is, emit an audit event `upstream.auto_disabled` with `(tenant_id, user_id, upstream_id, reason, streak_started_at)` and a notification. **v1**: the audit event is a structured zap log at INFO level; [Phase 12](phase-12-staff-backoffice.md) introduces the `audit_events` table + `audit.Append` writer and retrofits this emission to persist there. **future**: email via Zitadel's notification hooks.

- **Effect**: Phase 8's tool-visibility filter treats `AutoDisabledAt != NULL` the same as `Enabled=false` — the upstream's tools disappear from `tools/list` and `CallTool` returns the structured "re-link or re-enable required" MCP error. The round-tripper short-circuits before hitting the network if `AutoDisabledAt != NULL`.
- **Recovery**: the portal shows the auto-disabled banner with a one-click **Re-enable** (clears `AutoDisabledAt` + `ConsecutiveFailures`, lets the next request try again) or **Reconnect** (full `StartLink` flow, used when `NeedsRelink=true` is also set). Any successful subsequent call clears `AutoDisabledAt` automatically on its way through.
- **Background refresher** skips `AutoDisabledAt != NULL` rows just like it skips `NeedsRelink=true` ones — there's no point burning quota on a known-broken link until the user acts.

### One-shot OAuth state (Valkey-backed)

```
Key:   limen:upstream:state:<state_value>           (state_value = 32 random bytes, base64url)
Value: JSON { tenant_id, user_id, upstream_id, return_to, expires_at,
              pkce_verifier_ciphertext (AAD tenant|user|"upstream.pkce") }
TTL:   10 minutes (server-side; Valkey handles expiry, no janitor)
Read:  GETDEL — atomic one-shot consumption (replay finds nil)
```

The random `state_value` is what the upstream AS echoes back on `/callback`; Limen verifies the signed HMAC envelope **and** that the Valkey `GETDEL` returned a payload, in that order. A cookie-backed alternative is rejected because one-shot enforcement still requires server-side state (a cookie can be replayed by the browser).

### Tool visibility rule (handed to Phase 8)

Phase 8 will gate per-tool exposure by:

```go
visible := (strategy.RequiresLink() == false) || (userHasLinkFor(upstream, user) && link.Enabled && link.AutoDisabledAt == nil)
```

- `none` upstream → always visible.
- `static_header` in `tenant` mode → always visible (admin-supplied secret applies to all users).
- `mcp_spec` and `static_header` in `user` mode → visible only when the user has an `Enabled=true`, non-auto-disabled `UpstreamLink`.

This is exactly what makes the portal experience feel right: a freshly created tenant sees Atlassian listed as available, the portal prompts the user to connect (or to paste an API key), and the tools unlock afterward. The user can later toggle a link off to temporarily hide an upstream's tools from the LLM without re-doing the auth dance; or, if an upstream stays broken for that user, Limen auto-disables it and the portal surfaces a Re-enable / Reconnect CTA.

## Deliverables

- New files (under `internal/upstream/`): `strategy.go`, `handlers.go`, `refresher.go`, `mcpspec/{discovery,registrar,link,headers}.go`, `statichdr/{config,link,headers}.go`, `none/none.go`.
- Optional model addition: `UpstreamLink.FirstFailureAt *time.Time` (streak start) alongside the other health columns. No new DB-backed state table — OAuth state lives in Valkey.
- Schema migration adding `enabled BOOLEAN NOT NULL DEFAULT true`, `needs_relink BOOLEAN NOT NULL DEFAULT false`, `consecutive_failures INT NOT NULL DEFAULT 0`, `last_failure_at TIMESTAMPTZ`, `last_failure_reason TEXT NOT NULL DEFAULT ''`, and `auto_disabled_at TIMESTAMPTZ` to `upstream_links`.
- Updated `internal/transport/http.go` to mount `/t/{tenant}/upstream/{name}/*` routes.

## Security & operational notes

- **DCR happens once per `(tenant, upstream)`** — guarded by a uniqueness check on `UpstreamRegistration`. Re-running `Provision` after a successful DCR is a no-op (or refreshes registration metadata via RFC 7592 if needed). The same uniqueness guard applies to static-client provisioning: once `UpstreamRegistration` exists, `Provision` is a no-op regardless of whether DCR or a static `Config` produced it.
- **Static OAuth clients are operator-provisioned**. When an AS doesn't support DCR (GitHub being the canonical example), the operator registers a client through the upstream's own developer console, then passes `--client-id` / `--client-secret` (and any `--issuer` / `--authorization-endpoint` / `--token-endpoint` / `--scope` overrides needed) to `limen create-upstream`. The CLI encrypts the bundle into `UpstreamStrategyConfig.ConfigJSON` with AAD `tenant|""|"upstream.mcpspec.strategy_config"`. The portal's per-tenant admin UI grows the same affordance in Phase 9c.
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

- **PRM/DCR variations across vendors** — Atlassian is the reference for the full DCR path; GitHub is the reference for the static-client fallback. Some upstreams may deviate further (different scope naming, non-standard metadata). The discovery code is forgiving on optional fields, strict on the mandatory ones, and overlays `UpstreamStrategyConfig` overrides on top of whatever metadata it could fetch.
- **OAuth client secret may not be issued** for public client registrations — the model permits null `client_secret` and the token-exchange path treats public-client registrations with PKCE-only.
- **Long-lived refresher under `WithSuperuser`** is a privileged code path; keep it narrow and audited.

## Checklist

- [x] `internal/upstream/strategy.go` defines the `Strategy` interface (including `RequiresLink`) and `Registry`
- [x] Registry populated in `cmd/gateway/main.go` with `mcp_spec`, `static_header`, and `none` strategies _(wired in [internal/cli/serve_upstream.go](../../internal/cli/serve_upstream.go))_
- [x] `internal/upstream/mcpspec/discovery.go` fetches PRM + AS metadata with timeouts and caches the result; tries the RFC 9728 canonical well-known path, the legacy path-suffixed form, and a `WWW-Authenticate: resource_metadata="…"` hint in that order; overlays `UpstreamStrategyConfig` (issuer / endpoints) on top of network metadata
- [x] `internal/upstream/mcpspec/provision.go` performs DCR once per (tenant, upstream) when the AS advertises `registration_endpoint`; falls back to a static OAuth client read from `UpstreamStrategyConfig` otherwise (errors with an actionable message when neither is available); persists `UpstreamRegistration` either way
- [x] `internal/upstream/mcpspec/config.go` defines the static-client JSON payload (`client_id`, `client_secret`, optional `issuer` / `authorization_endpoint` / `token_endpoint` / `scopes`), with `EncodeConfig` / `loadConfig` helpers and AAD `tenant|""|"upstream.mcpspec.strategy_config"`
- [x] `internal/upstream/mcpspec/link.go` builds PKCE+`resource`+state authorize URL (appending any config-provided scopes) and handles token exchange
- [x] HMAC state is signed with a domain-separated key; persisted (or signed-cookied) with one-shot semantics _(AES-SIV envelope via [internal/upstream/oauthstate/oauthstate.go](../../internal/upstream/oauthstate/oauthstate.go), Valkey `GETDEL`)_
- [x] `internal/upstream/mcpspec/refresh.go` injects `Authorization: Bearer ...` and refreshes inline when within 60 s of expiry
- [x] `internal/upstream/statichdr/config.go` validates header name/template + mode; refuses unknown modes _(merged into [internal/upstream/statichdr/statichdr.go](../../internal/upstream/statichdr/statichdr.go))_
- [x] `internal/upstream/statichdr/headers.go` reads the tenant secret (tenant mode) or the user link (user mode) and formats the header
- [x] `static_header` user-mode `StartLink` returns a portal SPA URL (no third-party redirect); rotation overwrites the stored key atomically _(via `PersistUserSecret`; portal RPC wiring lands in Phase 9b)_
- [x] `static_header` `RequiresLink()` returns `false` in tenant mode, `true` in user mode
- [x] `internal/upstream/none/none.go` returns empty headers; `Provision` rejects upstreams that advertise PRM
- [x] `internal/upstream/handlers.go` exposes connect / callback / disconnect under `/t/{tenant}/upstream/{name}/*` behind `RequirePortalSession` _(callback HTTP route in [internal/transport/upstream.go](../../internal/transport/upstream.go); connect/disconnect ship as portal Connect-RPC mutations in Phase 9b)_
- [x] `internal/upstream/refresher.go` runs a single goroutine under `WithSuperuser(ctx)`, audited with a `// nolint:limen.superuser` comment, skips strategies whose `Maintain` is a no-op
- [x] Refresher interval and refresh window come from config (sensible defaults)
- [x] Tokens / API keys stored encrypted with AAD `tenant|user|"upstream.<kind>_token"` (and `tenant|""|"upstream.strategy_config"` for tenant-wide secrets)
- [x] `UpstreamLink.Enabled` field added (default `true`); migration shipped _(migration `00004_phase7_upstream.sql`)_
- [x] `UpstreamLink.NeedsRelink` field added (default `false`); migration shipped
- [x] `UpstreamLink` health-tracking columns added: `ConsecutiveFailures`, `FirstFailureAt`, `LastFailureAt`, `LastFailureReason`, `AutoDisabledAt`; migration shipped
- [x] Auto-disable rule: ≥5 consecutive failures over ≥15 min **or** `NeedsRelink=true` for ≥24 h → set `AutoDisabledAt=now()`; thresholds config-driven _(implemented in [internal/upstream/health.go](../../internal/upstream/health.go); thresholds in `config.UpstreamRefreshConfig`)_
- [x] Successful tool call or refresh atomically resets `ConsecutiveFailures` and clears `AutoDisabledAt` in the same DB tx that records the outcome _(refresh path; tool-call-side reset wires through Phase 8's round-tripper using the same `RecordSuccess` helper)_
- [x] Background refresher skips rows where `AutoDisabledAt IS NOT NULL` (as it already does for `NeedsRelink=true`)
- [x] Audit event `upstream_auto_disabled` emitted as a structured zap log with `(tenant_id, user_id, upstream_id, reason, streak_started_at)` _(persisted `audit_events` writer is owned by [Phase 12](phase-12-staff-backoffice.md), which retrofits this emission once the `audit.Append` helper exists)_
- [x] `mcp_spec` refresh path is centralized in one `refreshLocked` function, called by `Headers` (proactive), the round-tripper (reactive on 401, single retry), and `Maintain` (background) _(round-tripper reactive 401 path lands in [Phase 8](phase-08-per-tenant-injection.md) against this same function)_
- [x] Single-flight (`golang.org/x/sync/singleflight`) keyed by `link.PublicID` prevents concurrent refresh stampedes within a process
- [x] `SELECT ... FOR UPDATE SKIP LOCKED` on the link row prevents stampedes across processes
- [x] Refresh-token rotation: any of `access_token`, `refresh_token`, `expires_at`, `scopes` returned by the token endpoint is persisted; old refresh token is overwritten when a new one is issued
- [x] `invalid_grant` response sets `needs_relink=true`; portal surfaces a "Reconnect" CTA on those rows; background refresher skips rows where `needs_relink=true` _(portal CTA wiring is Phase 9b; the model flag and refresher skip are in place)_
- [x] Valkey-backed one-shot OAuth state (HMAC-signed envelope + encrypted PKCE verifier; `GETDEL`-style consumption; 10-minute TTL)
- [x] Unit tests for state signing, discovery, registration, token exchange, refresh, refresh-token rotation, single-flight collapse, `needs_relink` on `invalid_grant`, `none.Provision` rejection, `static_header` template rendering + mode dispatch

The four end-to-end integration tests that this phase originally listed have been moved to the phases that own the missing surface area:

- Full `mcp_spec` connect flow against an httptest stub → [Phase 8](phase-08-per-tenant-injection.md) (round-tripper asserts Headers injection on the upstream call).
- Reactive refresh on `401` — stub returns 401 once, then 200 → [Phase 8](phase-08-per-tenant-injection.md) (round-tripper owns the retry loop).
- `static_header` user-mode submit-key flow + enable/disable visibility → [Phase 9b](phase-09b-portal-spa.md) (portal Connect-RPC + SPA).
- Sustained-5xx auto-disable → Re-enable round-trip → [Phase 8](phase-08-per-tenant-injection.md) + [Phase 9b](phase-09b-portal-spa.md) (failure recorded by the round-tripper; recovery driven by `SetUpstreamLinkEnabled`).
