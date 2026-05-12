# AGENTS.md — `internal/auth`

## What this package is

Authentication / authorization middleware for the HTTP surface.

**Current state**: a Bearer-token extraction stub. JWT signature, `iss`,
`aud`, and `exp` validation are **not** implemented yet — do not deploy this
to production.

The full design lives in:

- [Phase 4 — Tenant, auth, session](../../docs/phases/phase-04-tenant-auth-session.md)
- [Phase 6 — Resource server](../../docs/phases/phase-06-resource-server.md)

## Design (target state)

Limen is an OAuth 2.1 / OIDC Relying Party for the portal and an MCP Resource
Server for `/t/{slug}/mcp`. **It does not issue tokens** — Zitadel does.

Middleware responsibilities in the target state:

1. Extract the `Authorization: Bearer <jwt>` header.
2. Validate the JWT against Zitadel's JWKS (cached, rotating).
3. Verify `iss` == configured `LIMEN_OIDC_ISSUER`.
4. Verify `aud` includes the MCP RS resource URI (RFC 8707 `resource`).
5. Verify `exp` / `nbf`.
6. Resolve the tenant from the URL path (`/t/{slug}/...`) and confirm the
   `urn:zitadel:iam:user:resourceowner:id` claim matches that tenant's
   `ZitadelOrgID`.
7. Pin the tenant into ctx via `storage.WithTenant`.
8. Extract project roles from `urn:zitadel:iam:org:project:roles` and stash
   them in ctx for downstream authorization checks.

## Conventions

- Tenant resolution and JWT validation happen **before** any DB session.
  Never call `storage.Session(ctx)` without first injecting the tenant.
- Authorization decisions key off Zitadel project roles, not a local `role`
  column — Limen has no user role table on purpose.
- 401 = no/invalid token; 403 = valid token, wrong tenant/role. Don't conflate.
- The middleware never logs raw tokens. Log only `sub`, `aud`, and the
  decision outcome.

## What this package is NOT

- Not an OAuth Authorization Server. DCR, token issuance, user/password
  management — all Zitadel's job.
- Not a session store. Portal sessions are server-side cookies issued by
  Limen _after_ a successful OIDC code exchange (Phase 4), stored in
  Postgres via `internal/storage`.
- Not a feature-flag / policy engine. Per-tenant policy lives elsewhere.

## What lives here over time

- `middleware.go` — current stub; becomes the JWT/JWKS validator (Phase 6).
- `oidc.go` (Phase 4) — code-exchange + portal session creation.
- `jwks.go` (Phase 6) — cached JWKS fetcher with rotation.
- `roles.go` (Phase 6) — Zitadel role-claim parsing helpers.
