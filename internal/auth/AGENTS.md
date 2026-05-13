# AGENTS.md — `internal/auth`

## What this package is

Authentication / authorization for the HTTP surface. Two distinct flows
share this package:

1. **Portal RP** (Phase 4, implemented): OIDC relying party for the
   browser — `internal/auth/oidc.go`, `state.go`.
2. **MCP Resource Server** (Phase 6, stub): JWT bearer validator for
   `/t/{slug}/mcp` — `internal/auth/middleware.go`.

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

## MCP Resource Server (Phase 6 — stub)

File: `middleware.go`.

**Current state**: Bearer-token extraction stub. JWT signature, `iss`,
`aud`, and `exp` validation are **not** implemented yet — do not deploy
this to production.

Target middleware responsibilities:

1. Extract the `Authorization: Bearer <jwt>` header.
2. Validate the JWT against Zitadel's JWKS (cached, rotating — shared
   with the portal RP's JWKS cache).
3. Verify `iss` == configured `LIMEN_OIDC_ISSUER`.
4. Verify `aud` includes the MCP RS resource URI (RFC 8707 `resource`).
5. Verify `exp` / `nbf`.
6. Resolve the tenant from the URL path (`/t/{slug}/...`) and confirm
   the `urn:zitadel:iam:user:resourceowner:id` claim matches that
   tenant's `ZitadelOrgID`.
7. Pin the tenant into ctx via `storage.WithTenant`.
8. Extract project roles from `urn:zitadel:iam:org:project:roles` and
   stash them in ctx for downstream authorization checks.

---

## Conventions

- Tenant resolution and credential validation happen **before** any DB
  session. Never call `storage.Session(ctx)` without first injecting the
  tenant.
- 401 = no/invalid credential; 403 = valid credential, wrong tenant or
  role. Don't conflate.
- The middleware never logs raw tokens. Log only `sub`, `aud`, and the
  decision outcome.

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
- `middleware.go` (Phase 6 — stub) — JWT/JWKS bearer validator for MCP RS.
- `jwks.go` (Phase 6) — shared cached JWKS fetcher with rotation (may
  reuse the `rp` package's `RemoteKeySet` instead).
- `roles.go` (Phase 6) — Zitadel role-claim parsing helpers (currently
  `ExtractRoles` lives in `oidc.go`; consolidate when MCP RS arrives).
