# Phase 4 — Tenant resolution, OIDC login, portal session

**Depends on**: Phases 1, 2, 3 (and the Zitadel stack from [Phase 0](phase-00-dev-environment.md))
**Unblocks**: Phases 5, 7, 9

## Goal

Bring the multi-tenant runtime to life:

1. Parse `/t/{tenant}/...` URLs into a `*Tenant` and place it in `ctx`. The `{tenant}` segment is the tenant's `PublicID` (a `tnt_<ULID>` string from [`internal/ids`](../../internal/ids/)) — there is no slug.
2. Authenticate portal users via **Zitadel as an OIDC provider** (Limen is the relying party — no local passwords).
3. Maintain a browser-facing portal session as a **per-tenant encrypted cookie carrying the OIDC ID + refresh tokens**, validated on every request by JWT signature + expiry against Zitadel's JWKS. No server-side session store; no `SessionService` round-trip in the hot path.

Local password auth (argon2id) is **not** in scope. Zitadel owns user credentials, MFA, password resets, email verification, the login UI, and session-level enforcement. Limen stores just enough of the user to scope its own data: a `User` row keyed by `(tenant_id, zitadel_subject)`.

## Self-service delegation to Zitadel Console

Limen is **not** an identity-management product. Every user-facing and tenant-admin-facing identity operation that Zitadel already ships as a [self-service feature](https://zitadel.com/docs/concepts/features/selfservice) is delegated to Zitadel's hosted UI at `<issuer>/ui/console`. Limen surfaces deep links into Console where useful, but never reimplements the screens.

The table below pins down the boundary so reviewers can reject any RPC / UI / CLI proposal that duplicates Zitadel:

| Surface area                                                           | Owner     | Where it happens                                                                                                                                              | Limen's role                                                                                                                                 |
| ---------------------------------------------------------------------- | --------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| **Login UI, MFA, password reset, email/phone verification**            | Zitadel   | Hosted login at `<issuer>` ([Login](https://zitadel.com/docs/concepts/features/selfservice#login))                                                            | OIDC RP; redirect in, parse callback, never see the password                                                                                 |
| **User profile** (name, email, phone, language, passkeys)              | Zitadel   | `<issuer>/ui/console/users/me` ([Profile](https://zitadel.com/docs/concepts/features/selfservice#profile))                                                    | Portal SPA renders a "Manage your account" link; no profile RPC in Limen                                                                     |
| **Logout**                                                             | Zitadel   | `end_session_endpoint` ([Logout](https://zitadel.com/docs/concepts/features/selfservice#logout))                                                              | `LogoutHandler` clears the Limen cookie + 302 to Zitadel `end_session`                                                                       |
| **Inviting users into a tenant org**                                   | **Limen** | Admin SPA `/t/{tenant}/admin/org/members` → `AdminService.InviteMember` ([Phase 9c](phase-09c-tenant-admin-spa.md))                                           | Owns the UI; the RPC is a pass-through to Zitadel User V2 `AddHumanUser` + Authorization V2 `CreateAuthorization` — no Limen mirror table    |
| **Changing a user's project role** (`owner`/`admin`/`member`)          | **Limen** | Admin SPA → `AdminService.UpdateMemberRole` ([Phase 9c](phase-09c-tenant-admin-spa.md))                                                                       | Owns the UI; pass-through to Zitadel Authorization V2 `UpdateAuthorization`. Role still appears in the next ID token; `RequireRole` reads it |
| **Removing / deactivating a user**                                     | **Limen** | Admin SPA → `AdminService.RemoveMember` ([Phase 9c](phase-09c-tenant-admin-spa.md))                                                                           | Owns the UI; pass-through to Zitadel Authorization V2 `DeleteAuthorization` + optional `User.Delete`                                         |
| **Org-level branding** (logo, colors, custom domain)                   | Zitadel   | Console `Settings > Branding` per org                                                                                                                         | None                                                                                                                                         |
| **Tenant-level external IdP federation** (OIDC / SAML / social)        | Zitadel   | Console `Identity Providers` per org ([Existing Identity / SSO](https://zitadel.com/docs/concepts/features/selfservice#existing-identity--sso--social-login)) | None. Limen drives the standard OIDC flow; Zitadel renders the SSO buttons                                                                   |
| **Org-level security policy** (login policy, lockout, MFA enforcement) | Zitadel   | Console `Settings > Login` / `Lockout` per org                                                                                                                | None                                                                                                                                         |
| **Service accounts, PATs, machine users**                              | Zitadel   | Console `Service Users`                                                                                                                                       | None                                                                                                                                         |
| **Creating a brand-new tenant** (= Zitadel org + Limen row)            | **Limen** | `limen create-tenant` CLI, or `AdminService.StartSignup` / `CompleteSignup` wizard ([Phase 9c](phase-09c-tenant-admin-spa.md))                                | Owns this end-to-end: creates the Zitadel org, the seed owner grant, and the Limen `Tenant` row in a single transaction                      |
| **Per-user upstream MCP linking** (OAuth dance + API-key paste)        | **Limen** | Portal SPA `/t/{tenant}/portal/` ([Phase 9b](phase-09b-portal-spa.md))                                                                                        | Owns it; this is the Limen domain Zitadel knows nothing about                                                                                |
| **Tenant-scoped upstream catalog CRUD**                                | **Limen** | Admin SPA `/t/{tenant}/admin/` ([Phase 9c](phase-09c-tenant-admin-spa.md))                                                                                    | Owns it                                                                                                                                      |

**Rule of thumb**: if Zitadel's Console can already do it for an `ORG_OWNER` of the tenant's org **and Limen does not need a first-class UI for it**, Limen does not build a UI, an RPC, or a CLI command for it. Member management is the one explicit exception — invite / role / remove flows are first-class in the admin SPA but implemented as thin pass-throughs to Zitadel's User V2 + Authorization V2 APIs with **zero** mirror tables. Everything else (IdP federation, branding, login/lockout policy, profile/MFA/passkey enrollment) surfaces in the admin SPA as a deep-link card into Console. This is the _delegated administration_ pattern Zitadel documents in [Administrators in delegation](https://zitadel.com/docs/concepts/features/selfservice#administrators-in-delegation): the SaaS operator owns the Limen project; each tenant's `ORG_OWNER` self-serves their own org from Console.

## Design

### URL shape

Every tenant-scoped route lives under `/t/{tenant}/...`. The `{tenant}` segment is the tenant's `PublicID` (a `tnt_<ULID>` string minted in [`internal/ids`](../../internal/ids/)) — not a human-chosen slug. Two consequences:

- No reserved-word list, no regex validation at resolution time — the resolver rejects any segment that is not a structurally valid `tnt_<ULID>` with a 404 before touching the database.
- Customer URLs always start with the `tnt_` prefix, so the staff backoffice (see [Phase 12](phase-12-staff-backoffice.md)) can mount cleanly outside `/t/...` (e.g. at top-level `/_staff/...`) with no collision risk by construction.

### Tenant ↔ Zitadel organization

Each Limen tenant is bound 1:1 to a **Zitadel organization**. On tenant creation Limen calls the Zitadel Management API to create the org (or links to an existing one). The Zitadel `org_id` is persisted on the `Tenant` row.

After the tenant row is committed, `create-tenant` calls `ManagementService.SetOrgMetadata(org_id, key="limen_tenant_id", value=tenant.PublicID)` so the two sides can be cross-referenced from Console without consulting Limen's database.

The implication for auth: a Zitadel-issued JWT for a user carries the user's resource-owner org id in the `urn:zitadel:iam:user:resourceowner:id` claim. Limen verifies that this matches `tenant.zitadel_org_id` for the path-tenant. **This is the canonical cross-tenant defense at the authentication layer** — RLS is the second line of defense at the data layer.

### `internal/tenancy/resolver.go`

```go
func Resolve(ctx, tenantPublicID) (*Tenant, error)    // lookup by PublicID via adminDB (Tenant table is not RLS-scoped)
func TenantFromContext(ctx) (*Tenant, bool)

// Middleware mounted on the /t/{tenant}/* subrouter
func RequireTenant(store *storage.Store) func(http.Handler) http.Handler
```

The middleware:

1. Reads `chi.URLParam(r, "tenant")` (URL pattern `/t/{tenant}/...`).
2. Calls `Resolve`. The resolver validates the parameter is a structurally valid `tnt_<ULID>` via `ids.MustParse(ids.PrefixTenant, ...)` and returns `ErrNotFound` for any malformed input. On not-found → 404 with a generic message (do not leak existence).
3. Calls `storage.WithTenant(ctx, tenant.ID)` and continues.

### `internal/auth/oidc.go` — OIDC relying party

Library: `github.com/zitadel/oidc/v3` (relying-party side: `rp` subpackage).

For the Management / User API calls (CLI tenant + invite provisioning) we use the **official Zitadel Go SDK** `github.com/zitadel/zitadel-go/v3`. The SDK ships generated gRPC clients (`api.ManagementService()`, `api.UserService()`, `api.OrganizationService()`, etc.) on top of an authenticated `*client.Client`. We build the client once at startup with `client.New(ctx, zitadel.New(domain), client.WithAuth(authOption))` and inject it into the CLI subcommands. `authOption` is one of:

- `client.PAT(token)` for dev (the bootstrap PAT from [Phase 0](phase-00-dev-environment.md))
- `client.DefaultServiceUserAuthentication(keyPath, ...)` for production (private-key JWT)

`internal/zitadel/` is a thin domain wrapper around the SDK — typed helpers like `CreateOrganization`, `AddHumanUser`, `AddUserGrant` (used by `create-tenant` to mint the seed `owner` and by `AdminService` member RPCs for invite / role / remove pass-through), plus `ListOrgUsers`, `UpdateUserGrant`, `DeleteUserGrant`, `DeleteUser` for the directory listing and mutations — so callers depend on a small Limen-shaped surface, not on the generated protobuf types directly. We do **not** hand-roll the HTTP/JSON transport, and we do **not** wrap Zitadel's SessionService or IdP-management APIs: those are Console-driven (see _Self-service delegation_). Session liveness is JWT signature + `exp` against Zitadel's JWKS.

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
GET /auth/login?tenant={publicID}&return_to={path}   →  store target tenant + return_to in a signed state cookie; redirect to Zitadel authorize
GET /auth/callback?code=...&state=...                 →  exchange code, validate ID token, resolve tenant, create Zitadel session, set portal cookie, redirect to /t/{publicID}/portal/{return_to}
POST /auth/logout                                  →  terminate the Zitadel session, clear cookie, redirect to Zitadel end_session, then back to /
```

The reason these routes are tenant-agnostic at the URL level: Zitadel issues a single redirect URI per app registration. Putting the tenant in the path forces a redirect URI per tenant, which doesn't scale. Instead the tenant `PublicID` is in the OIDC `state` (signed) and the org binding is verified on the returned token.

Implementation note: `state` is an HMAC-signed bundle of `(nonce, tenant, return_to, expires_at)`, keyed off the encryption key from [Phase 2](phase-02-crypto-config.md) with a domain-separation tag (`"oidc.state"`).

#### Token validation on callback

1. Exchange code with Zitadel's token endpoint (PKCE for public-client deployments).
2. Validate ID token signature against Zitadel's JWKS (cached).
3. Validate `iss`, `aud`, `exp`, `nonce`.
4. Extract `sub` (Zitadel user id) and `urn:zitadel:iam:user:resourceowner:id` (Zitadel org id).
5. Look up the `Tenant` by the signed state's `tenant` (the `PublicID`). Check `tenant.zitadel_org_id == claim.org_id`. Mismatch → 403 (the user is not a member of this tenant).
6. Upsert `User` keyed by `(tenant_id, zitadel_subject)`. Populate `email`, `name` from claims. **No role is stored** — authorization is driven by the `urn:zitadel:iam:org:project:roles` claim on the token, see "Roles" below.
7. Seal `(id_token, refresh_token)` into the portal cookie (next section). No call to any Zitadel session API.

### `internal/auth/oidc.go` — portal session (OIDC tokens)

#### Why a cookie at all?

Zitadel owns the authoritative session — issuance, MFA state, idle/absolute timeouts, revocation. The cookie Limen sets is **not a parallel session store**; it carries the OIDC tokens Zitadel issued so subsequent requests can re-verify the user offline. HTTP is stateless, so every request to `limen.example.com` has to carry _something_ identifying the user, and Zitadel's own session cookie is scoped to `auth.limen.example.com` and opaque to relying parties — the browser will never send it to Limen and Limen could not look it up if it did. So Limen issues its own cookie.

We picked an `HttpOnly; Secure; SameSite=Lax; Path=/t/<tenant>` cookie carrying the encrypted ID + refresh tokens over the alternative — a bearer token held in SPA memory or storage — for three reasons:

1. **XSS posture.** `HttpOnly` keeps the credential unreachable from JavaScript. A bearer-in-storage approach exposes both access and refresh tokens to any XSS that lands on the SPA.
2. **BFF fit.** Limen is already a backend-for-frontend: it fans out to upstream MCPs with credentials those MCPs gave us, and never hands those credentials to the browser. The cookie-BFF pattern is the recommended browser-app shape in the OAuth WG's _OAuth 2.0 for Browser-Based Apps_ draft for exactly this case.
3. **Cross-tenant isolation.** `Path=/t/<tenant>` means a browser carrying tenant A's session physically cannot send it on a request to tenant B, even on the same domain. A bearer in JS would have no equivalent — the SPA could accidentally attach it to any URL.

The cookie is a same-origin credential; the SPA and Limen API share `limen.example.com` (Phase 9b, Phase 11) so `SameSite=Lax` is sufficient and we never need `SameSite=None` or CORS-with-credentials. Zitadel remains the single source of truth: revocation propagates whenever the access/refresh token next needs to talk to Zitadel (refresh time), and the JWKS-based verification means a stolen cookie cannot outlive the token's `exp` without a working refresh token.

#### Mechanics

Limen does **not** persist its own session state, and does **not** call Zitadel's SessionService. It is a textbook OIDC relying party using `github.com/zitadel/oidc/v3/pkg/client/rp`:

- **Issuance**: on a successful OIDC callback, `rp.CodeExchange[*oidc.IDTokenClaims]` returns `id_token + refresh_token + claims`. The handler verifies `claims["urn:zitadel:iam:user:resourceowner:id"]` matches the tenant's `zitadel_org_id`, then seals `{id_token, refresh_token, expiresAt}` into the cookie.
- **Cookie payload**: an authenticated-encrypted blob (AES-SIV with AAD `{TenantID: <tenant-public-id>, Kind: "portal.oidc.tokens"}`, from [Phase 2](phase-02-crypto-config.md)) containing `{idToken, refreshToken, expiresAt}`. The cookie itself is a single opaque base64 string. Cross-tenant replay fails on AAD mismatch even before the tokens are inspected.
- **Cookie attributes**: `Set-Cookie: limen_portal=<blob>; Path=/t/<tenant>; HttpOnly; Secure; SameSite=Lax; Max-Age=<refresh-ttl>`. The `Path=/t/<tenant>` scope means a browser carrying a session for tenant A _cannot_ leak it to tenant B even on the same domain.
- **Validation** (`RequireSession`): decrypt cookie → `rp.VerifyIDToken[*oidc.IDTokenClaims]` against the cached JWKS (issuer signature + `exp` + `aud`). No network call on the hot path; the JWKS is fetched lazily and cached by the `rp` package's `RemoteKeySet`. On `exp` failure, attempt `rp.RefreshTokens[*oidc.IDTokenClaims]` once, rewrite the cookie with the new pair, and continue. On any other failure (signature, audience, refresh denied), clear the cookie and 302 to `/t/{tenant}/auth/login`.
- **Logout**: clear the portal cookie and redirect to `rp.EndSession(...)`'s `end_session_endpoint` URL with `id_token_hint` so Zitadel terminates its own session and clears the IdP cookie. No DB row to delete.
- **No DB tables, no janitor**: session lifetime is owned upstream; nothing to sweep on Limen's side.

Middlewares exported:

```go
func (*OIDC) RequireSession() func(http.Handler) http.Handler          // populates ctx with *oidc.IDTokenClaims
func (*OIDC) RequireRole(roles ...string) func(http.Handler) http.Handler  // owner|admin|member, read from Zitadel project-roles claim
```

### Roles (delegated to Zitadel project roles)

Authorization roles live in **Zitadel**, not in Limen's database. Each tenant's Zitadel organization carries user grants against the shared Limen project, with one of the three project roles defined at bootstrap (Phase 0): `owner`, `admin`, `member`.

Flow:

- **Source of truth**: a user's role for a tenant is the project role attached to the user grant in that tenant's Zitadel org. Tenant administrators manage grants through the Limen admin SPA (`AdminService.InviteMember` / `UpdateMemberRole` / `RemoveMember`), which pass through to Zitadel User V2 + Authorization V2 — see the _Self-service delegation_ section above. The CLI's `create-tenant` issues the very first `owner` grant; subsequent mutations go through the admin SPA RPCs.
- **Transport**: the role appears in the ID/access token under the [`urn:zitadel:iam:org:project:roles`](https://zitadel.com/docs/apis/openidoauth/claims#urn-zitadel-iam-org-project-roles) claim, scoped to the tenant org. Limen requests it via the `urn:zitadel:iam:org:project:id:<project-id>:aud` scope (already in the portal scope list — see [Phase 0](phase-00-dev-environment.md)).
- **At the portal**: the OIDC callback parses the claim. `RequireSession` re-verifies the ID token on every request and exposes the full `*oidc.IDTokenClaims` on ctx; `RequireRole(...)` reads the project-roles claim from those live claims and enforces.
- **At the MCP RS** ([Phase 6](phase-06-resource-server.md)): the same claim is parsed off the bearer token. (MCP RS itself does not currently need role-based gating beyond auth, but the role is available to handlers that want it.)
- **Owner invariant** ("a tenant must always have at least one `owner`"): Zitadel enforces this on its side for any grant mutation initiated through the Console or its APIs. Limen's `create-tenant` always grants `owner` to the seed user. Limen does not re-implement the invariant because Limen no longer offers a grant-mutation surface of its own.
- **Cache TTL**: roles are read fresh off the ID token on every request. A role change in Zitadel propagates on the next ID-token refresh (driven by the access token's `exp`, typically a few minutes), or immediately on next login.

### CLI bootstrap (`cmd/gateway/main.go` + `internal/cli`)

The gateway binary is structured around [**Cobra**](https://github.com/spf13/cobra) for the command tree and [**Viper**](https://github.com/spf13/viper) for flag/env binding. `cmd/gateway/main.go` builds the root command and delegates to subcommands under `internal/cli/`:

```
limen serve [--config config.yaml]                                # default; runs the HTTP server
limen create-tenant --name "Acme Corp" --owner-email admin@acme.com
limen migrate                                                     # runs AutoMigrate + goose (Phase 3)
```

The CLI surface is intentionally minimal. **User invitations, role changes, password resets, MFA enrollment, and IdP federation are all driven from the [Zitadel Console](https://zitadel.com/docs/concepts/features/selfservice)**, not from Limen. The CLI exists to bootstrap a brand-new tenant (an operation that creates _both_ a Zitadel org _and_ a Limen `Tenant` row, which Zitadel cannot do on its own) and to run database migrations. Day-2 user management is Zitadel's job.

Conventions:

- A persistent `--config` flag (also bound to `LIMEN_CONFIG`) points at the YAML file. The Phase 2 loader (`internal/config.Load`) is still the source of truth for the YAML — it retains the `${ENV:-default}` substitution semantics that Viper does not natively offer. Viper sits in front of Cobra purely for **flag + env binding** on CLI-only inputs.
- Subcommand flags use kebab-case (`--owner-email`); env overrides use `LIMEN_` prefix with underscore separators (e.g. `LIMEN_OWNER_EMAIL`). Viper's `AutomaticEnv()` + `SetEnvKeyReplacer` handles the translation.
- Help text comes from each command's `Short` / `Long` strings. `cobra.MinimumNArgs` / `MarkFlagRequired` enforces shape; we do **not** hand-roll `flag` parsing.
- Logging in CLI mode goes to stderr (zap dev encoder) so stdout stays clean for any structured output a subcommand emits.

Subcommand contracts:

- `create-tenant`:
  1. Validates inputs (`--name` required, owner email or `--owner-user-id` / `--zitadel-org-id` required).
  2. Calls Zitadel Management API to create an organization named after the tenant.
  3. Persists the `Tenant` row with `zitadel_org_id`. The tenant's `PublicID` (a `tnt_<ULID>`) is minted automatically and is the only externally visible identifier.
  4. Creates a Zitadel "human user" with the owner email; Zitadel emails the initial password setup link via SMTP (MailHog in dev — see [Phase 0](phase-00-dev-environment.md)). The new owner uses Zitadel's hosted UI to set the password and (optionally) enroll MFA — Limen is not involved.
  5. Calls `UserService.AddUserGrant(userId, projectId, orgId, ["owner"])` so the seed user is granted the `owner` project role for this tenant org.
  6. Persists the `User` row in Limen with `zitadel_subject` pre-populated from the Zitadel API response. **No role column** — Limen never stores it.
  7. Calls `ManagementService.SetOrgMetadata(org_id, key="limen_tenant_id", value=tenant.PublicID)` so the Zitadel side mirrors the Limen-side identifier; failures here are logged but non-fatal (the tenant row is already committed).
  8. Prints the new tenant's `PublicID` and the Zitadel Console deep-link (`<issuer>/ui/console?org=<orgId>`) so the operator can hand it to the new owner; from there the owner self-serves additional invites, role changes, IdP federation, and branding.

After `create-tenant`, no further Limen CLI commands are needed for user management. `invite-user`, `reset-password`, `change-role`, `remove-user`, etc. are **not** Limen subcommands — they live in the Zitadel Console / API.

### Middleware composition

```
/auth/callback                               → public (single root redirect URI; tenant PublicID recovered from signed state)

/t/{tenant}/                                 → RequireTenant
  ├─ /auth/login                              → public (302 to Zitadel)
  ├─ /auth/logout                             → public (clears cookie, 302 to Zitadel end_session)
  ├─ /portal/                                 → SPA shell (public — auth happens via /auth/login redirect)
  └─ /portal/api/portal.v1.PortalService/*   → RequireSession → role-aware Connect-RPC (Phase 9b)
```

OAuth + MCP routes (Phases 5, 6) attach under the same `/t/{tenant}/...` subrouter; they have their own auth (Zitadel-issued bearer for MCP, PRM is public).

## Deliverables

- New files:
  - `internal/tenancy/resolver.go`
  - `internal/auth/oidc.go` (RP + handlers + middleware + encrypted token cookie)
  - `internal/auth/state.go` (signed state cookie helper)
  - `internal/zitadel/client.go` (constructs the SDK `*client.Client` from config)
  - `internal/zitadel/users.go` (thin UserService wrapper around the SDK; used by `create-tenant` only)
  - `internal/zitadel/orgs.go` (thin OrgService wrapper around the SDK, used here for tenant creation)
  - `internal/cli/root.go` (Cobra root + persistent flags, Viper binding)
  - `internal/cli/serve.go` (extracts today's `main` body into a `serve` subcommand)
  - `internal/cli/create_tenant.go`
  - `internal/cli/migrate.go` (thin wrapper around `storage.Migrate` so operators can run migrations standalone)
- Modified files:
  - `cmd/gateway/main.go` — shrinks to a Cobra root command bootstrap; all real work lives in `internal/cli`.
  - `internal/transport/http.go` — mount `/auth/*` routes; mount `/t/{tenant}` subrouter with `RequireTenant`.
  - `internal/storage/models.go` — `Tenant.ZitadelOrgID`, `User.ZitadelSubject` (no `PasswordHash`, no `PortalSession`, no `Invitation`).

## Security & operational notes

- **No tenant enumeration**: `/t/unknown/anything` returns the same 404 page regardless of whether the tenant exists. The `/auth/callback` org-mismatch case returns a generic "access denied" without disclosing whether the tenant exists.
- **Cookie path scoping** is the deliberate cross-tenant isolation primitive in the browser. Reviewers must verify the `Path=/t/{tenant}` attribute in every place a portal cookie is issued.
- **PKCE for the Portal SPA app** even if it's confidential — defense in depth.
- **`nonce` validation** on the ID token is mandatory; reject tokens without one.
- **State cookie** is HMAC-signed and short-lived (10 min); reject reuse via a one-shot marker (or accept the small race and rely on TTL).
- **CSRF**: portal API endpoints (Phase 9b Connect-RPC) use `Content-Type: application/connect+json`, which browsers preflight → CSRF-resistant.
- **Logout**: Limen's logout deletes the local session and redirects to Zitadel's end-session endpoint; otherwise a user's Zitadel session lingers and any new login returns immediately without a credential prompt.
- **Password resets / MFA enforcement / email verification** are policies configured in Zitadel — Limen doesn't reimplement them.

## Future work (deferred — not in Phase 4 scope)

The Phase 4 surface intentionally stops at the minimum needed for the portal to log a user in. The following workflows are explicitly **out of scope for this phase**:

1. **Self-serve SaaS signup** (creating a brand-new tenant from a public web form rather than the CLI) — delivered by [Phase 9c](phase-09c-tenant-admin-spa.md) (`AdminService.StartSignup` + `CompleteSignup`, captcha-gated, signed signup token, MailHog round-trip in dev). Both the CLI and the signup wizard share the same `zitadel.CreateOrg` + `AddHumanUser` + `AddUserGrant(owner)` primitives.

User invitations, role changes, member removal, and tenant-level external IdP federation (OIDC / SAML / social) are **not deferred work** — they are explicitly out of Limen's scope and live in [Zitadel Console](https://zitadel.com/docs/concepts/features/selfservice). The Limen admin SPA ([Phase 9c](phase-09c-tenant-admin-spa.md)) renders a deep-link card pointing operators at Console for these operations.

## Verification

- State signing / verification roundtrip; reject tampered state.
- HTTP-level test against a stub OIDC provider (testcontainers-zitadel optional in CI, else a hand-rolled `op` server):
  - `GET /t/<publicID>/auth/login` redirects to the provider's authorize endpoint with PKCE.
  - Provider returns to `/auth/callback`; Limen exchanges code, validates ID token, sets the session cookie with `Path=/t/<publicID>`, redirects to `/t/<publicID>/portal/`.
  - The cookie's attributes match expectations.
  - Same cookie sent to `/t/<otherPublicID>/portal/...` is rejected (different `Path`).
  - A returned token whose `org_id` claim ≠ tenant's `zitadel_org_id` → 403.
- CLI test: `create-tenant` creates the Zitadel org, the Limen tenant row, the owner `User`, and mirrors the tenant `PublicID` into the Zitadel org metadata under `limen_tenant_id`.

## Risks

- **Zitadel API availability at request time**: discovery, JWKS, and the token endpoint are called at login and on refresh. The hot path (`RequireSession` on every portal request) only needs the cached JWKS and runs offline. If Zitadel is down, new logins and refreshes fail — accepted cost of delegation — but already-authenticated users continue to work until their ID token's `exp` passes.
- **Org-id claim drift**: Zitadel's claim names are stable, but a major version upgrade could rename them. The claim extraction lives in one place — change-detect via a unit test using a sample token.
- **State cookie one-shot enforcement** adds DB writes per login; accept the cost or rely on TTL alone in v1.

## Checklist

- [x] `internal/tenancy/resolver.go` exports `Resolve`, `TenantFromContext`, `RequireTenant`; the resolver rejects any URL segment that is not a structurally valid `tnt_<ULID>` with `ErrNotFound`
- [x] Tenant lookup uses `adminDB` (Tenant table is not RLS-scoped)
- [x] `internal/auth/oidc.go` builds a single `rp.RelyingParty` from config
- [x] `/auth/login`, `/auth/callback`, `/auth/logout` routes implemented and tested
- [x] State is HMAC-signed with `(nonce, tenant, return_to, expires_at)`; tampering rejected
- [x] ID-token validation: signature, `iss`, `aud`, `exp`, `nonce`
- [x] Tenant binding enforced by matching the `urn:zitadel:iam:user:resourceowner:id` claim to `tenant.zitadel_org_id`
- [x] `User` upserted by `(tenant_id, zitadel_subject)` on successful callback
- [x] **Roles delegated to Zitadel**: no `role` column on `User`; roles read from the `urn:zitadel:iam:org:project:roles` claim on the live (verified) ID token every request
- [x] **User/permission management delegated to Zitadel Console**: no Limen CLI / RPC / UI for invite, role change, member removal, password reset, MFA enrollment, or IdP federation (verified by `grep` in CI for those terms in `internal/cli/` and the proto files)
- [x] `internal/auth/oidc.go` exchanges the auth code with `rp.CodeExchange`, verifies the tenant binding, and seals `{idToken, refreshToken, expiresAt}` into an AES-SIV-encrypted cookie (AAD `{TenantID: <tenant-public-id>, Kind: "portal.oidc.tokens"}`)
- [x] Session cookie has `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/t/<tenant>` attributes
- [x] `RequireSession` middleware decrypts cookie, calls `rp.VerifyIDToken` against the cached JWKS, transparently calls `rp.RefreshTokens` on `exp` failure, populates ctx with `*oidc.IDTokenClaims`
- [x] `RequireRole(...)` middleware enforces role membership against the project-roles claim from those live claims
- [x] Logout clears the portal cookie and redirects to `rp.EndSession`'s URL with `id_token_hint`
- [x] CLI `create-tenant` provisions Zitadel org + owner user + `AddUserGrant(owner)` + Limen rows, then mirrors the tenant `PublicID` into the Zitadel org metadata under `limen_tenant_id`; prints the new `PublicID` and the Zitadel Console deep-link on success
- [x] `cmd/gateway/main.go` builds a Cobra root command with `serve`, `create-tenant`, `migrate` subcommands; Viper binds the persistent `--config` flag and CLI-only flags to `LIMEN_*` env overrides
- [x] Unit tests for state signing, session lifecycle, cookie attributes, org-binding mismatch
- [x] HTTP integration test against a stub OIDC issuer for the full login flow
