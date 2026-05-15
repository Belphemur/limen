# AGENTS.md — `internal/auth`

## What this package is

Authentication / authorization for the HTTP surface. Two distinct flows
share this package:

1. **Portal RP** (Phase 4, implemented): OIDC relying party for the
   browser — `internal/auth/oidc.go`, `state.go`.
2. **MCP Resource Server** (Phase 6, implemented): JWT bearer validator
   for `/t/{slug}/mcp` — `internal/auth/middleware.go` (`MCPAuth`).

Limen never issues tokens and never sees passwords. Zitadel is the
authoritative OIDC provider, user store, and session manager.

The full design lives in:

- [Phase 4 — Tenant, auth, session](../../docs/phases/phase-04-tenant-auth-session.md)
- [Phase 6 — Resource server](../../docs/phases/phase-06-resource-server.md)

---

## Portal RP (Phase 4 — implemented)

Files: `oidc.go`, `state.go`, plus tests.

Limen is the relying party for the browser portal. Zitadel renders the
login UI, enforces MFA, owns the user store, and issues the tokens.
Limen's job is to drive the code flow, carry the resulting tokens in a
per-tenant cookie, and verify them offline on every request.

Routes:

- `/t/{slug}/auth/login` — mint signed state, 302 to Zitadel `/authorize`.
- `/auth/callback` (root, single redirect URI) — verify state, exchange
  code, check tenant binding (`urn:zitadel:iam:user:resourceowner:id` ==
  `tenant.ZitadelOrgID`), seal tokens into the portal cookie, 302 back to
  `/t/{slug}{return_to}`.
- `/t/{slug}/auth/logout` — clear cookie, 302 to `rp.EndSession(...)` with
  `id_token_hint`.

Cookies:

- `limen_portal` — AAD-encrypted (`{TenantID: slug, Kind:
  "portal.oidc.tokens"}`) JSON `{idToken, refreshToken, expiresAt}`.
  `Path=/t/{slug}; HttpOnly; Secure; SameSite=Lax`.
- `limen_state` — HMAC-signed state from `StateSigner`.
  `Path=/auth/callback; HttpOnly; Secure; SameSite=Lax; Max-Age=600`.

`RequireSession` middleware (mounted under `RequireTenant`):

1. Decrypt the portal cookie with the slug as AAD. Cross-tenant replay
   fails on AAD mismatch.
2. `rp.VerifyIDToken[*oidc.IDTokenClaims](...)` against the cached JWKS.
   No network call on the hot path.
3. On `exp` failure, transparently call `rp.RefreshTokens(...)` once and
   rewrite the cookie. On any other failure (signature, audience, refresh
   denied), clear the cookie and 302 to `/t/{slug}/auth/login`.
4. Pin `*oidc.IDTokenClaims` on ctx via `WithClaims`.

`RequireRole(want...)` reads
`claims.Claims["urn:zitadel:iam:org:project:roles"]` (shape:
`map[role]map[orgID]orgName`) and intersects with `want`. Authorization
decisions key off Zitadel project roles, not a local `role` column —
Limen has no user role table on purpose.

What this flow is **not**:

- Not a parallel session store. No `sessions` table. No `SessionService`
  round-trip on the hot path.
- Not an authorization server. Token issuance, DCR, user management —
  all delegated to Zitadel.

---

## MCP Resource Server (Phase 6 — implemented)

File: `middleware.go`.

`MCPAuth` validates inbound bearer access tokens against Zitadel's JWKS
in-process. Built once at startup with `NewMCPAuth(ctx, cfg, metadata,
store, logger)` — discovery against `cfg.Issuer` resolves `jwks_uri` and
the verifier is configured RS256-only.

`RequireMCPAuth` is the chi middleware, mounted under
`tenancy.RequireTenant` by `internal/transport/MountMCPRS`:

1. Extract `Authorization: Bearer <jwt>`. Missing → 401.
2. `op.VerifyAccessToken[*MCPAccessClaims]` — checks `iss`, signature
   (RS256), `exp`, `nbf`. Failure → 401.
3. Verify `aud` contains `cfg.Audience`. Mismatch → 401.
4. Verify `urn:zitadel:iam:user:resourceowner:id` claim equals the URL
   tenant's `ZitadelOrgID`. Mismatch → **403**.
5. Resolve the local `users` row by `(tenant_id, zitadel_subject)` via
   `store.Session(ctx)`. Missing → 401 (no auto-provision on the RS
   path; provisioning happens portal-side).
6. Stash `*storage.User` + `*MCPAccessClaims` on ctx via
   `MCPUserFromContext` / `MCPClaimsFromContext`.

Every 401/403 carries the RFC 9728 `WWW-Authenticate` challenge built by
`internal/mcprs`, pointing at
`/t/{tenant}/mcp/.well-known/oauth-protected-resource`.

---

## Conventions

- Tenant resolution and credential validation happen **before** any DB
  session. Never call `storage.Session(ctx)` without first injecting the
  tenant.
- 401 = no/invalid credential; 403 = valid credential, wrong tenant or
  role. Don't conflate.
- The middleware never logs raw tokens. Log only `sub`, `aud`, and the
  decision outcome.
- **Zitadel SDK / API policy:** any direct Zitadel SDK call from this
  package (e.g. JWKS fetch, future RS-side metadata or session lookups)
  MUST use the **v2 resource-based services** (`oidc/v2`, `session/v2`,
  `webkey/v2`, …). The legacy v1 services (`management/`, `admin/`,
  `auth/`, `system/`) and any SDK method carrying a `// Deprecated:`
  comment are off-limits — `staticcheck` (SA1019) is enforced by the
  pre-commit `golangci-lint` run and must stay green. See
  [`internal/zitadel/AGENTS.md`](../zitadel/AGENTS.md#zitadel-sdk--api-policy--read-before-touching-code)
  for the full policy and v2 mapping table, and Zitadel's own guidance
  at <https://zitadel.com/docs/apis/introduction>.

## What this package is NOT

- Not an OAuth Authorization Server. DCR, token issuance, user/password
  management — all Zitadel's job. (The MCP-client-facing AS proxy lives
  in `internal/oauthproxy` per Phase 5 — a different surface entirely.)
- Not a session store.
- Not a feature-flag / policy engine. Per-tenant policy lives elsewhere.

## What lives here over time

- `oidc.go` (Phase 4 — done) — portal RP, handlers, `RequireSession`,
  `RequireRole`, claims ctx helpers.
- `state.go` (Phase 4 — done) — signed state cookie helper.
- `middleware.go` (Phase 6 — done) — `MCPAuth` JWT bearer validator for
  the MCP RS, RFC 9728 challenge on every 401/403.
- `jwks.go` (Phase 6) — shared cached JWKS fetcher with rotation (may
  reuse the `rp` package's `RemoteKeySet` instead).
- `roles.go` (Phase 6) — Zitadel role-claim parsing helpers (currently
  `ExtractRoles` lives in `oidc.go`; consolidate when MCP RS arrives).
