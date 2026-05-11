# Phase 4 — Tenant resolution, OIDC login, portal session

**Depends on**: Phases 1, 2, 3 (and the Zitadel stack from [Phase 0](phase-00-dev-environment.md))
**Unblocks**: Phases 5, 7, 9

## Goal

Bring the multi-tenant runtime to life:

1. Parse `/t/{slug}/...` URLs into a `*Tenant` and place it in `ctx`.
2. Authenticate portal users via **Zitadel as an OIDC provider** (Limen is the relying party — no local passwords).
3. Maintain a browser-facing portal session whose authoritative state lives in Zitadel's [SessionService](https://zitadel.com/docs/reference/api/session/zitadel.session.v2.SessionService.CreateSession). Limen carries only an opaque cookie scoped to `/t/{slug}` holding the Zitadel `sessionId` + `sessionToken`.

Local password auth (argon2id) is **not** in scope. Zitadel owns user credentials, MFA, password resets, email verification, and session-level enforcement. Limen stores just enough of the user to scope its own data: a `User` row keyed by `(tenant_id, zitadel_subject)`.

## Design

### URL shape and reserved slugs

Every tenant-scoped route lives under `/t/{slug}/...`. The slug regex is `^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$` (lowercase, 1–32 chars, no leading/trailing hyphen). A reserved-slug list blocks names that would collide with platform paths:

```
api, oauth, oidc, portal, t, admin, static, mcp, .well-known, public, health, metrics, login, logout, register, robots.txt, favicon.ico, auth
```

Reserved checks fire at tenant creation (CLI / admin RPC), not at resolution time — once a tenant exists, the slug is safe by construction.

### Tenant ↔ Zitadel organization

Each Limen tenant is bound 1:1 to a **Zitadel organization**. On tenant creation Limen calls the Zitadel Management API to create the org (or links to an existing one). The Zitadel `org_id` is persisted on the `Tenant` row alongside the slug.

The implication for auth: a Zitadel-issued JWT for a user carries the user's resource-owner org id in the `urn:zitadel:iam:user:resourceowner:id` claim. Limen verifies that this matches `tenant.zitadel_org_id` for the path-tenant. **This is the canonical cross-tenant defense at the authentication layer** — RLS is the second line of defense at the data layer.

### `internal/tenancy/resolver.go`

```go
func Resolve(ctx, slug) (*Tenant, error)    // lookup by slug via adminDB (Tenant table is not RLS-scoped)
func TenantFromContext(ctx) (*Tenant, bool)

// Middleware mounted on the /t/{tenant}/* subrouter
func RequireTenant(store *storage.Store) func(http.Handler) http.Handler
```

The middleware:

1. Reads `chi.URLParam(r, "tenant")` (URL pattern `/t/{tenant}/...`).
2. Calls `Resolve`. On not-found → 404 with a generic message (do not leak existence).
3. Calls `storage.WithTenant(ctx, tenant.ID)` and continues.

### `internal/auth/oidc.go` — OIDC relying party

Library: `github.com/zitadel/oidc/v3` (relying-party side: `rp` subpackage).

Configuration (from `internal/config`):

```go
type OIDCConfig struct {
    Issuer       string  // e.g. https://auth.limen.example.com
    ClientID     string  // Portal SPA app client_id from Zitadel
    ClientSecret string  // empty for PKCE public client
    Scopes       []string // openid, profile, email, offline_access, urn:zitadel:iam:org:project:id:{projectID}:aud
    RedirectURI  string  // <base_url>/auth/callback (tenant-agnostic — see below)
}
```

At startup, a single `rp.RelyingParty` is built. Tenant binding happens **after** authentication: the JWT's `urn:zitadel:iam:user:resourceowner:id` claim picks the tenant.

#### Routes (mounted at the root, not under `/t/{tenant}`)

```
GET /auth/login?tenant={slug}&return_to={path}   →  store target tenant + return_to in a signed state cookie; redirect to Zitadel authorize
GET /auth/callback?code=...&state=...             →  exchange code, validate ID token, resolve tenant, create Zitadel session, set portal cookie, redirect to /t/{slug}/portal/{return_to}
POST /auth/logout                                  →  terminate the Zitadel session, clear cookie, redirect to Zitadel end_session, then back to /
```

The reason these routes are tenant-agnostic at the URL level: Zitadel issues a single redirect URI per app registration. Putting the tenant in the path forces a redirect URI per tenant, which doesn't scale. Instead the tenant slug is in the OIDC `state` (signed) and the org binding is verified on the returned token.

Implementation note: `state` is an HMAC-signed bundle of `(nonce, slug, return_to, expires_at)`, keyed off the encryption key from [Phase 2](phase-02-crypto-config.md) with a domain-separation tag (`"oidc.state"`).

#### Token validation on callback

1. Exchange code with Zitadel's token endpoint (PKCE for public-client deployments).
2. Validate ID token signature against Zitadel's JWKS (cached).
3. Validate `iss`, `aud`, `exp`, `nonce`.
4. Extract `sub` (Zitadel user id) and `urn:zitadel:iam:user:resourceowner:id` (Zitadel org id).
5. Look up the `Tenant` by the signed state's `slug`. Check `tenant.zitadel_org_id == claim.org_id`. Mismatch → 403 (the user is not a member of this tenant).
6. Upsert `User` keyed by `(tenant_id, zitadel_subject)`. Populate `email`, `name` from claims. **No role is stored** — authorization is driven by the `urn:zitadel:iam:org:project:roles` claim on the token, see "Roles" below.
7. Issue a portal session (next section).

### `internal/auth/session.go` — portal session (Zitadel-backed)

Limen does **not** persist its own session state. It delegates to Zitadel's [SessionService v2](https://zitadel.com/docs/reference/api/session/zitadel.session.v2.SessionService.CreateSession):

- **Issuance**: on a successful OIDC callback, call `SessionService.CreateSession` with the authenticated user (`checks.user.userId = <zitadel sub>`). Zitadel returns `(sessionId, sessionToken)` and its own `expirationDate` (configured at the Zitadel instance/project level).
- **Cookie payload**: an authenticated-encrypted blob (AES-256-GCM with AAD `tenant|user|"portal_session"`, from [Phase 2](phase-02-crypto-config.md)) containing `{sessionId, sessionToken, userId, tenantId, expiresAt}`. Encryption keeps the Zitadel session token off the wire as plaintext even if the cookie leaks to a logging proxy. The cookie itself is a single opaque base64 string.
- **Cookie attributes**: `Set-Cookie: <name>=<blob>; Path=/t/<slug>; HttpOnly; Secure; SameSite=Lax; Max-Age=<ttl>`. The `Path=/t/<slug>` scope means a browser carrying a session for tenant A _cannot_ leak it to tenant B even on the same domain.
- **Validation** (`RequirePortalSession`): decrypt cookie → if `expiresAt < now` short-circuit reject; otherwise call `SessionService.GetSession(sessionId, sessionToken)`. A short positive cache (60 s, keyed by `sessionId`) avoids one Zitadel call per RPC. Cache invalidation on logout. A failed `GetSession` (revoked or expired upstream) clears the cookie and 401s.
- **Logout**: call `SessionService.DeleteSession(sessionId, sessionToken)` to terminate the Zitadel-side session, then clear the cookie, then redirect to Zitadel's end-session URL so the IdP cookie is cleared too.
- **No DB tables, no janitor**: session lifetime is owned upstream; nothing to sweep on Limen's side.

Middlewares exported:

```go
func RequirePortalSession(store) func(http.Handler) http.Handler   // populates ctx with *User + roles
func RequireRole(roles ...string) func(http.Handler) http.Handler  // owner|admin|member, read from Zitadel project-roles claim
```

### Roles (delegated to Zitadel project roles)

Authorization roles live in **Zitadel**, not in Limen's database. Each tenant's Zitadel organization carries user grants against the shared Limen project, with one of the three project roles defined at bootstrap (Phase 0): `owner`, `admin`, `member`.

Flow:

- **Source of truth**: a user's role for a tenant is the project role attached to the user grant in that tenant's Zitadel org. Operators can manage it from Zitadel's hosted console, the portal admin UI ([Phase 9](phase-09-portal-spa.md)), or the CLI — all three end up calling Zitadel's Management/User API.
- **Transport**: the role appears in the ID/access token under the [`urn:zitadel:iam:org:project:roles`](https://zitadel.com/docs/apis/openidoauth/claims#urn-zitadel-iam-org-project-roles) claim, scoped to the tenant org. Limen requests it via the `urn:zitadel:iam:org:project:id:<project-id>:aud` scope (already in the portal scope list — see [Phase 0](phase-00-dev-environment.md)).
- **At the portal**: the OIDC callback parses the claim and stores `roles []string` alongside the rest of the session payload in the encrypted cookie. `RequirePortalSession` re-hydrates them into ctx; `RequireRole(...)` enforces.
- **At the MCP RS** ([Phase 6](phase-06-resource-server.md)): the same claim is parsed off the bearer token. (MCP RS itself does not currently need role-based gating beyond auth, but the role is available to handlers that want it.)
- **Owner invariant** ("a tenant must always have at least one `owner`"): enforced at the call sites that mutate user grants — `-create-tenant` always grants `owner` to the seed user; `RemoveMember` and `UpdateMemberRole` (Phase 9) refuse the operation if it would leave zero owners. Limen does this by listing the org's user grants from Zitadel before applying the change.
- **Cache TTL** matches the portal-session cache (60 s); a role change in Zitadel takes at most one minute to propagate to active sessions, or is immediate on next login.

### CLI bootstrap (`cmd/gateway/main.go`)

```
limen -create-tenant slug=acme name="Acme Corp" owner-email=admin@acme.com
limen -invite-user tenant=acme email=alice@acme.com role=member
```

- `-create-tenant`:
  1. Validates slug.
  2. Calls Zitadel Management API to create an organization named after the tenant.
  3. Persists the `Tenant` row with `zitadel_org_id`.
  4. Creates a Zitadel "human user" with the owner email; Zitadel emails the initial password setup link via SMTP (MailHog in dev — see [Phase 0](phase-00-dev-environment.md)).
  5. Calls `UserService.AddUserGrant(userId, projectId, orgId, ["owner"])` so the seed user is granted the `owner` project role for this tenant org.
  6. Persists the `User` row in Limen with `zitadel_subject` pre-populated from the Zitadel API response. **No role column** — Limen never stores it.
- `-invite-user`:
  1. Creates a Zitadel human user in the tenant's org via `UserService.AddHumanUser`.
  2. Calls `UserService.AddUserGrant(userId, projectId, orgId, [<role>])` where `<role>` is the `role=` flag (`owner`, `admin`, or `member`; defaults to `member`).
  3. Calls [`UserService.CreateInviteCode`](https://zitadel.com/docs/reference/api/user/zitadel.user.v2.UserService.CreateInviteCode) with `sendCode=true` so Zitadel emails the invitation link via SMTP (MailHog in dev). No invite token is stored in Limen.
  4. Persists the Limen `User` row (no role column) with `zitadel_subject` from the API response.

Resending an invite or revoking it goes through `UserService.ResendInviteCode` / `UserService.DeactivateUser`; Limen exposes these via the portal (Phase 9) without keeping its own invite tracking.

`-reset-password` is **not** a Limen CLI subcommand anymore — operators do it through Zitadel's admin console (or via Zitadel's CLI / API). Limen never sees passwords.

### Middleware composition

```
/auth/login                                  → public
/auth/callback                               → public
/auth/logout                                 → RequirePortalSession (optional — accepts no session)

/t/{tenant}/                                 → RequireTenant
  ├─ /portal/                                → SPA shell (public — auth happens via /auth/login redirect)
  ├─ /portal/api/portal.v1.PortalService/*   → RequirePortalSession → role-aware Connect-RPC (Phase 9)
  └─ /portal/api/logout                      → RequirePortalSession
```

OAuth + MCP routes (Phases 5, 6) attach under the same `/t/{tenant}/...` subrouter; they have their own auth (Zitadel-issued bearer for MCP, PRM is public).

## Deliverables

- New files:
  - `internal/tenancy/resolver.go`
  - `internal/auth/oidc.go`
  - `internal/auth/session.go` (Zitadel SessionService client + cookie encrypt/decrypt)
  - `internal/auth/state.go` (signed state cookie helper)
  - `internal/zitadel/sessions.go` (thin SessionService wrapper)
  - `internal/zitadel/users.go` (thin UserService wrapper, used here for invites)
  - `internal/cli/create_tenant.go`
  - `internal/cli/invite_user.go`
- Modified files:
  - `cmd/gateway/main.go` — CLI flag parsing.
  - `internal/transport/http.go` — mount `/auth/*` routes; mount `/t/{tenant}` subrouter with `RequireTenant`.
  - `internal/storage/models.go` — `Tenant.ZitadelOrgID`, `User.ZitadelSubject` (no `PasswordHash`, no `PortalSession`, no `Invitation`).

## Security & operational notes

- **No tenant enumeration**: `/t/unknown/anything` returns the same 404 page regardless of whether the slug exists. The `/auth/callback` org-mismatch case returns a generic "access denied" without disclosing whether the tenant exists.
- **Cookie path scoping** is the deliberate cross-tenant isolation primitive in the browser. Reviewers must verify the `Path=/t/{slug}` attribute in every place a portal cookie is issued.
- **PKCE for the Portal SPA app** even if it's confidential — defense in depth.
- **`nonce` validation** on the ID token is mandatory; reject tokens without one.
- **State cookie** is HMAC-signed and short-lived (10 min); reject reuse via a one-shot marker (or accept the small race and rely on TTL).
- **CSRF**: portal API endpoints (Phase 9 Connect-RPC) use `Content-Type: application/connect+json`, which browsers preflight → CSRF-resistant.
- **Logout**: Limen's logout deletes the local session and redirects to Zitadel's end-session endpoint; otherwise a user's Zitadel session lingers and any new login returns immediately without a credential prompt.
- **Password resets / MFA enforcement / email verification** are policies configured in Zitadel — Limen doesn't reimplement them.

## Verification

- Slug validation unit tests: accepts/rejects representative inputs; reserved slugs rejected.
- State signing / verification roundtrip; reject tampered state.
- HTTP-level test against a stub OIDC provider (testcontainers-zitadel optional in CI, else a hand-rolled `op` server):
  - `GET /auth/login?tenant=acme` redirects to the provider's authorize endpoint with PKCE.
  - Provider returns to `/auth/callback`; Limen exchanges code, validates ID token, sets the session cookie with `Path=/t/acme`, redirects to `/t/acme/portal/`.
  - The cookie's attributes match expectations.
  - Same cookie sent to `/t/other/portal/...` is rejected (different `Path`).
  - A returned token whose `org_id` claim ≠ tenant's `zitadel_org_id` → 403.
- CLI test: `-create-tenant` creates the Zitadel org, the Limen tenant row, and the owner `User`; idempotency (re-run with same slug) errors out cleanly.

## Risks

- **Zitadel API availability at request time**: discovery, JWKS, and `SessionService` are called during login and on every (non-cached) portal request. If Zitadel is down, the portal fails — accepted cost of delegation. Limen caches JWKS aggressively (Phase 6) and session validations for 60 s (above) to limit hot-path dependence.
- **Org-id claim drift**: Zitadel's claim names are stable, but a major version upgrade could rename them. The claim extraction lives in one place — change-detect via a unit test using a sample token.
- **State cookie one-shot enforcement** adds DB writes per login; accept the cost or rely on TTL alone in v1.

## Checklist

- [ ] Slug regex and reserved-slug list defined and unit-tested (`auth` added to the reserved list)
- [ ] `internal/tenancy/resolver.go` exports `Resolve`, `TenantFromContext`, `RequireTenant`
- [ ] Tenant lookup uses `adminDB` (Tenant table is not RLS-scoped)
- [ ] `internal/auth/oidc.go` builds a single `rp.RelyingParty` from config
- [ ] `/auth/login`, `/auth/callback`, `/auth/logout` routes implemented and tested
- [ ] State is HMAC-signed with `(nonce, slug, return_to, expires_at)`; tampering rejected
- [ ] ID-token validation: signature, `iss`, `aud`, `exp`, `nonce`
- [ ] Tenant binding enforced by matching the `urn:zitadel:iam:user:resourceowner:id` claim to `tenant.zitadel_org_id`
- [ ] `User` upserted by `(tenant_id, zitadel_subject)` on successful callback
- [ ] **Roles delegated to Zitadel**: no `role` column on `User`; roles read from the `urn:zitadel:iam:org:project:roles` claim on every login and stored in the encrypted session cookie
- [ ] `at least one owner` invariant enforced at every grant-mutating call site (CLI + Phase 9 portal RPCs) by listing user grants in the tenant org before applying the change
- [ ] `internal/auth/session.go` calls Zitadel `SessionService.CreateSession` on callback and stores `{sessionId, sessionToken, userId, tenantId, roles, expiresAt}` in an AES-256-GCM-encrypted cookie
- [ ] Session cookie has `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/t/<slug>` attributes
- [ ] `RequirePortalSession` middleware decrypts cookie, validates via `SessionService.GetSession` (with 60 s positive cache), populates ctx with `*User` + roles
- [ ] `RequireRole(...)` middleware enforces role membership against the roles claim from the session
- [ ] Logout calls `SessionService.DeleteSession`, clears the cookie, and redirects to Zitadel's end-session URL
- [ ] CLI `-create-tenant` provisions Zitadel org + owner user + `AddUserGrant(owner)` + Limen rows; idempotent failure on duplicate slug
- [ ] CLI `-invite-user` provisions a Zitadel user in the tenant org, calls `AddUserGrant(<role>)`, then `UserService.CreateInviteCode` (with `sendCode=true`), and creates the Limen `User` row
- [ ] `cmd/gateway/main.go` dispatches CLI subcommands before starting the server
- [ ] Unit tests for slug validation, state signing, session lifecycle, cookie attributes, org-binding mismatch
- [ ] HTTP integration test against a stub OIDC issuer for the full login flow
