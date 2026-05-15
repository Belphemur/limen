# Phase 6 — Limen as MCP Resource Server

**Depends on**: Phase 5 (Zitadel integration + AS metadata proxy)
**Unblocks**: Phase 8 (tool injection now operates inside the authenticated tenant + user ctx)

## Goal

Make `/t/{tenant}/mcp` an MCP-spec-compliant Resource Server: advertise Protected Resource Metadata (RFC 9728), enforce bearer-token authentication on every MCP request, return a proper `WWW-Authenticate` 401 challenge with a `resource_metadata` pointer, and validate JWT access tokens **in-process** against the **single Zitadel JWKS**.

This phase replaces the stub in `internal/auth/middleware.go` with real validation logic and re-mounts the MCP handler from the current global `/mcp` route to the per-tenant `/t/{tenant}/mcp` path behind `tenancy.RequireTenant` + `RequireMCPAuth`. Tenant binding at the RS happens via the `urn:zitadel:iam:user:resourceowner:id` claim (the user's resource-owner org id, already aliased as `orgIDClaim` in `internal/auth/oidc.go` — reuse, don't redefine), matched against `tenant.zitadel_org_id` resolved from `tenancy.TenantFromContext`.

### State of the codebase (entering Phase 6)

- `internal/auth/middleware.go` is a stub: `Middleware{logger, jwksURL, audience}` + `RequireAuth` + `extractBearerToken` + `validateToken` returning `"not yet implemented"`. Constants `claimsKey`, helpers `SetClaims` / `GetClaims` exist and can be kept or replaced by typed ctx keys.
- `internal/config/config.go` carries a `AuthConfig{JWKSURL, Audience}` block marked legacy. Phase 6 should delete it — `oidc.issuer` (Phase 4) is the issuer; the audience belongs alongside `zitadel.project_id` (the Zitadel project the access token is minted for).
- `internal/transport/http.go` mounts the MCP SSE handler at `/mcp` and `/mcp/` on the **root router**, with no tenant scoping and no auth. Phase 6 moves this mount under `r.Route("/t/{tenant}/mcp", ...)` behind `tenancy.RequireTenant` + `RequireMCPAuth`, with the PRM endpoint declared before the catch-all.
- `github.com/go-jose/go-jose/v4` and `github.com/zitadel/oidc/v3` are already in `go.mod` (pulled by Phase 4). No new deps required.
- `internal/storage/storagetest.OpenMigrated` (added in Phase 5 tests) gives Phase 6 a one-call testcontainer + migrated schema for integration tests.

## Design

### `internal/mcprs/metadata.go` — Protected Resource Metadata (RFC 9728)

Served at:

```
GET /t/{tenant}/mcp/.well-known/oauth-protected-resource
```

Response shape:

```json
{
  "resource": "https://limen.example.com/t/acme/mcp",
  "authorization_servers": ["https://limen.example.com/t/acme/oauth"],
  "bearer_methods_supported": ["header"],
  "resource_documentation": "https://limen.example.com/docs/mcp",
  "scopes_supported": ["openid", "profile", "email", "offline_access"]
}
```

`resource` is derived from `server.base_url` + the tenant's `PublicID` (no trailing slash). `authorization_servers` points at Limen's per-tenant **AS metadata wrapper** (Phase 5) — that document in turn declares Zitadel as the actual issuer and surfaces Limen's DCR proxy `registration_endpoint`. PRM is **public** — no auth required to fetch it.

### `internal/mcprs/challenge.go` — `WWW-Authenticate` builder

Every 401 from the MCP RS includes the challenge:

```
WWW-Authenticate: Bearer realm="mcp",
  resource_metadata="https://limen.example.com/t/acme/mcp/.well-known/oauth-protected-resource",
  error="invalid_token",
  error_description="..."
```

Error codes (RFC 6750 + RFC 9728):

- No bearer present → `error="invalid_request"` (or omitted), HTTP 401
- Expired / bad signature → `error="invalid_token"`, HTTP 401
- Insufficient scope → `error="insufficient_scope"`, HTTP 403

The body of a 401 is intentionally minimal (just an `error` JSON), all signaling lives in the header per the MCP spec guidance.

### `internal/auth/middleware.go` — JWT validation

The stub is replaced by:

```go
type Middleware struct {
    Store        *storage.Store
    JWKSResolver JWKSResolver   // fetches and caches Zitadel JWKS (single source)
    Issuer       string          // configured Zitadel issuer, e.g. https://auth.limen.example.com
    Audience     string          // configured MCP RS audience (Zitadel app project id)
    Clock        func() time.Time
}

func (m *Middleware) RequireMCPAuth(next http.Handler) http.Handler
```

Validation steps for each request to `/t/{tenant}/mcp`:

1. Extract `Authorization: Bearer <token>`. Missing → 401 with challenge.
2. Parse JWT (header + payload + signature). Bad shape → 401.
3. Verify `iss` equals the configured Zitadel issuer. Anything else → 401.
4. Fetch JWKS from Zitadel via `JWKSResolver` (in-memory cache, TTL = 5 min, refresh-on-`kid`-miss).
5. Verify signature with the key matching `kid`. Failure → 401.
6. Verify `exp`, `nbf`, `iat` (with 30 s clock skew).
7. Verify `aud` contains the configured MCP RS audience (the Zitadel project audience id). Mismatch → 401.
8. Extract `urn:zitadel:iam:user:resourceowner:id` claim. Match against `tenant.zitadel_org_id` from ctx. Mismatch → 403 (cross-tenant attempt).
9. Optionally verify a required scope (e.g. `openid`) is present.
10. Look up `User` by `(tenant_id, zitadel_subject=sub)`. Missing user → 401 (the token is for a Zitadel user not yet provisioned in this Limen tenant; portal login via `internal/transport/portal.go` is the canonical provisioning trigger — the MCP path deliberately does **not** auto-create users so an attacker with a valid Zitadel token can't create rows in a tenant they never logged in to).
11. Stash `*User`, scopes, and the raw token's `jti` (if present) into ctx.
12. Continue.

### JWKS + access-token validation

**Preferred path**: use `github.com/zitadel/oidc/v3/pkg/client/rs.NewResourceServer` + `rs.Introspect`/`rs.VerifyAccessToken`, or `op.NewAccessTokenVerifier`, to avoid reimplementing JWKS caching, `kid` selection, algorithm allowlists, and clock-skew handling — Phase 4's portal flow already uses the same library family for ID-token verification, so this keeps the verification surface uniform.

If the upstream verifier is too opinionated for our needs, the fallback is a minimal local resolver:

```go
type JWKSResolver struct {
    HTTPClient *http.Client
    Cache      sync.Map  // issuer → cachedJWKS
}
```

- Cache TTL: 5 min.
- On `kid` miss, do one immediate refetch (key rotation without waiting for TTL).
- HTTP fetch with a 3 s timeout, redirects disabled.
- Algorithm allowlist: `RS256` only.
- Single Zitadel issuer → single cache entry.

### Routing

```
/t/{tenant}/mcp/.well-known/oauth-protected-resource  → public (Phase 6)
/t/{tenant}/mcp                                       → RequireMCPAuth → MCP handler (Phase 8)
/t/{tenant}/mcp/                                      → same
```

The PRM endpoint is registered **before** the catch-all MCP handler so it isn't shadowed.

### `Mcp-Session-Id` is not authentication

The MCP transport uses `Mcp-Session-Id` for session continuity. Limen must:

- Never derive identity from it.
- Re-validate the bearer token on every request, regardless of whether `Mcp-Session-Id` is present.
- Bind sessions, if used, to `(tenant_id, user_id, mcp_session_id)` so a token swap across users can't continue the same MCP session.

This is documented in the route handler comments and verified by a test.

### Package layout

```
internal/mcprs/
├── metadata.go      // PRM endpoint
└── challenge.go     // WWW-Authenticate builder

internal/auth/
└── middleware.go    // RequireMCPAuth replaces the previous stub
```

## Deliverables

- New files: `internal/mcprs/metadata.go`, `internal/mcprs/challenge.go`.
- Rewritten `internal/auth/middleware.go` (the file exists today as a stub).
- New transport helper `internal/transport/mcprs.go` exposing `MountMCPRS(r chi.Router, MCPRSDeps{...})` that wires PRM + `RequireMCPAuth` + the existing `MCPServer` under `/t/{tenant}/mcp` (analogous to `MountOAuthProxy`).
- Removal of the legacy top-level `/mcp` route registered by `MCPServer.Mount` (or rework `Mount` to mount under a caller-supplied subrouter).
- Drop the legacy `AuthConfig` struct from `internal/config/config.go`; add `zitadel.project_audience` (or reuse `zitadel.project_id`) for the `aud` check.
- No new module deps: `github.com/go-jose/go-jose/v4` and `github.com/zitadel/oidc/v3` are already in tree.

## Security & operational notes

- **Strict `iss`, `aud`, and `org_id` checks** — `iss`+`aud` prove the token came from our Zitadel and is for our MCP RS; the `org_id` claim is what binds it to a specific tenant. Skipping any of the three breaks isolation. Note: Phase 5's AS metadata advertises `resource_indicators_supported: true`, but RFC 8707 per-resource `aud` values are best-effort across Zitadel versions — tenant isolation must rest on the `org_id` claim, **not** on `aud` containing the per-tenant resource URI.
- **`kid` is required**; reject tokens without a `kid` to prevent key-substitution bugs.
- **Algorithm allowlist**: only `RS256` (Zitadel default); reject `none`, `HS256`, etc.
- **Token replay / `jti`**: a per-tenant LRU of recent `jti`s catches reuse within a short window. v1 deliberately skips this for simplicity; revisit if abuse surfaces.
- **PRM is public on purpose** — it's a discovery document; do not put secret-bearing fields in it.
- **Constant-time path for token comparison is unnecessary** (JWT is self-signed; comparison happens cryptographically). Just don't log the token.

## Verification

- **No header**: `GET /t/acme/mcp` → 401 with `WWW-Authenticate: Bearer realm="mcp", resource_metadata="..."`.
- **Bad signature**: tampered token → 401 with `error="invalid_token"`.
- **Wrong `iss`**: token from a different issuer → 401.
- **Wrong `aud`**: token whose `aud` is unrelated to the MCP RS → 401.
- **Wrong `org_id`**: valid Zitadel token issued for tenant A used at `/t/B/mcp` → 403.
- **Expired `exp`**: 401.
- **Valid token**: handler reached; ctx contains correct `*User`.
- **PRM endpoint**: `GET /t/acme/mcp/.well-known/oauth-protected-resource` returns expected JSON without auth.
- **Discovery chain** (end-to-end with Phase 5): an MCP client hits `/t/acme/mcp` → gets 401 with `resource_metadata` → fetches PRM → fetches AS metadata → registers via DCR → drives authorize → token → re-hits `/t/acme/mcp` → 200.

## Risks

- **JWKS fetch loop**: if Limen and the JWKS endpoint are in the same process behind a single mux and the mux is blocked (e.g. exhausted handler goroutines), JWKS resolution stalls and amplifies the outage. Use a separate `http.Client` with a short timeout and consider a special-case shortcut for self-hosted issuers later.
- **Clock skew across replicas**: 30 s tolerance baked in; document NTP requirement.
- **Cache invalidation on key rotation**: the kid-miss → immediate refetch policy covers this. Test by rotating keys in a unit test.

## Checklist

- [x] Drop legacy `AuthConfig` from `internal/config/config.go`; add `zitadel.mcp_resource_audience` for the `aud` check
- [x] `internal/transport/mcprs.go` exposes `MountMCPRS` and is wired from `internal/cli/serve.go`
- [x] Remove the global `/mcp` mount from `internal/transport/http.go` (`MCPServer` now exposes `SSEHandler()` / `MessageHandler()` and the dynamic base path resolves per-tenant); MCP only reachable under `/t/{tenant}/mcp`
- [x] `internal/mcprs/metadata.go` exposes PRM at `/t/{tenant}/mcp/.well-known/oauth-protected-resource`
- [x] PRM response includes correct `resource`, `authorization_servers`, `scopes_supported`
- [x] `internal/mcprs/challenge.go` constructs the `WWW-Authenticate` header with `resource_metadata` for every 401/403
- [x] `internal/auth/middleware.go` rewritten with full JWT validation pipeline (`MCPAuth` / `RequireMCPAuth`)
- [x] `iss` checked against the configured Zitadel issuer (via `op.NewAccessTokenVerifier`)
- [x] `aud` checked against the configured MCP RS audience (`zitadel.mcp_resource_audience`)
- [x] `urn:zitadel:iam:user:resourceowner:id` claim checked against `tenant.zitadel_org_id` (cross-tenant defense → 403)
- [x] Algorithm allowlist enforced (`op.WithSupportedAccessTokenSigningAlgorithms("RS256")`)
- [x] `kid`-based key selection via `rp.NewRemoteKeySet` with caching + miss-driven refresh
- [x] `*User` loaded by `(tenant_id, zitadel_subject)` and placed in ctx for downstream handlers (no auto-provision on RS path)
- [x] `Mcp-Session-Id` explicitly **not** used for identity; comment + test enforces this
- [x] PRM route registered before the auth group (no shadowing)
- [x] Integration test: inbound discovery chain (401 → PRM → authorization_servers link → token → /mcp 200). Full Zitadel-backed end-to-end pass is deferred to Phase 10.
- [x] Integration test: cross-tenant rejection via `org_id` mismatch
- [x] Unit tests for each failure mode (no header, bad sig, expired, wrong iss, wrong aud, wrong org_id, unprovisioned user)
