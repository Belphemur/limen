# Phase 6 — Limen as MCP Resource Server

**Depends on**: Phase 5 (Zitadel integration + AS metadata proxy)
**Unblocks**: Phase 8 (tool injection now operates inside the authenticated tenant + user ctx)

## Goal

Make `/t/{tenant}/mcp` an MCP-spec-compliant Resource Server: advertise Protected Resource Metadata (RFC 9728), enforce bearer-token authentication on every MCP request, return a proper `WWW-Authenticate` 401 challenge with a `resource_metadata` pointer, and validate JWT access tokens **in-process** against the **single Zitadel JWKS**.

This phase replaces the stub in `internal/auth/middleware.go` with real validation logic. Tenant binding at the RS happens via the `urn:zitadel:iam:user:resourceowner:id` claim (the user's resource-owner org id), matched against `tenant.zitadel_org_id` resolved from the URL path.

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
10. Look up `User` by `(tenant_id, zitadel_subject=sub)`. Missing user → 401 (the token is for a Zitadel user not yet provisioned in this Limen tenant; the portal-login path is the canonical provisioning trigger).
11. Stash `*User`, scopes, and the raw token's `jti` (if present) into ctx.
12. Continue.

### `JWKSResolver`

```go
type JWKSResolver struct {
    HTTPClient *http.Client
    Cache      sync.Map  // issuer → cachedJWKS
}

type cachedJWKS struct {
    Keys      *jose.JSONWebKeySet
    FetchedAt time.Time
}

func (r *JWKSResolver) Get(ctx, issuer string, kid string) (*jose.JSONWebKey, error)
```

- Cache TTL: 5 min.
- On `kid` miss inside the cached set, do one immediate refetch (handles key rotation without waiting for TTL).
- HTTP fetch has a tight timeout (3 s) and uses `http.Client` with redirects disabled.
- Single issuer (Zitadel) means a single cache entry — straightforward.

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
- New helper module dep: `gopkg.in/go-jose/go-jose.v2` (or `github.com/go-jose/go-jose/v4`) for JWS parsing + JWKS. `zitadel/oidc` already pulls it in.

## Security & operational notes

- **Strict `iss`, `aud`, and `org_id` checks** — `iss`+`aud` prove the token came from our Zitadel and is for our MCP RS; the `org_id` claim is what binds it to a specific tenant. Skipping any of the three breaks isolation.
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

- [ ] `internal/mcprs/metadata.go` exposes PRM at `/t/{tenant}/mcp/.well-known/oauth-protected-resource`
- [ ] PRM response includes correct `resource`, `authorization_servers`, `scopes_supported`
- [ ] `internal/mcprs/challenge.go` constructs the `WWW-Authenticate` header with `resource_metadata` for every 401
- [ ] `internal/auth/middleware.go` rewritten with full JWT validation pipeline
- [ ] `iss` checked against the configured Zitadel issuer
- [ ] `aud` checked against the configured MCP RS audience
- [ ] `urn:zitadel:iam:user:resourceowner:id` claim checked against `tenant.zitadel_org_id` (cross-tenant defense)
- [ ] Algorithm allowlist enforced (`RS256` only)
- [ ] `kid`-based key selection via `JWKSResolver` with caching + miss-driven refresh
- [ ] `*User` upserted/loaded by `(tenant_id, zitadel_subject)` and placed in ctx for downstream handlers
- [ ] `Mcp-Session-Id` explicitly **not** used for identity; comment + test enforces this
- [ ] PRM route registered before catch-all MCP handler (no shadowing)
- [ ] Integration test: full inbound discovery chain (401 → PRM → AS metadata → DCR proxy → authorize on Zitadel → token → /mcp 200)
- [ ] Integration test: cross-tenant rejection via `org_id` mismatch
- [ ] Unit tests for each failure mode (no header, bad sig, expired, wrong iss, wrong aud, wrong org_id)
