# Phase 9b — Tenant administrative portal (Connect-RPC + Vue 3 SPA)

**Depends on**: Phases 4 (portal session, OIDC RP, role claim), 7 (upstream catalog), 9 (shared `web/` codebase, build pipeline, Connect-RPC infrastructure)
**Unblocks**: nothing (sister phase to 9 — together they cover the customer-facing surface)

## Goal

Split the customer-facing web surface into **two SPAs** sharing one `web/` codebase and one build pipeline but mounted on distinct URL paths and lazy-loaded as separate route bundles:

| Phase  | Path                | Audience                       | Bundle            |
| ------ | ------------------- | ------------------------------ | ----------------- |
| **9**  | `/t/{tenant}/portal/` | Every authenticated user       | `web/src/portal/` |
| **9b** | `/t/{tenant}/admin/`  | Tenant `owner` + `admin` roles | `web/src/admin/`  |
| 12     | `/t/_staff/portal/` | SaaS operator (`super_admin`)  | `web/src/staff/`  |

Phase 9 handles the **user**'s view of their own tenant (link/unlink upstreams, see their MCP clients, profile). Phase 9b handles the **tenant administrator**'s view of the **Limen-domain** admin surface: upstream catalog CRUD, tenant settings, and the public self-serve signup flow that bootstraps a brand-new tenant.

**Member management, role grants, password / MFA enrollment, and external IdP federation are intentionally _not_ part of this phase — they are delivered by [Zitadel Console](https://zitadel.com/docs/concepts/features/selfservice) for every tenant `ORG_OWNER`. The admin SPA renders a "Manage members & SSO in Zitadel Console" card with a deep-link instead of reimplementing those screens.** See the _Self-service delegation_ table in [Phase 4](phase-04-tenant-auth-session.md) for the full boundary.

Splitting the bundle at the route-loader level means a `member` browsing the customer portal never downloads the admin code, and an unauthenticated visitor to `/signup` never downloads either gated bundle.

## Design

### Where the admin SPA lives

```
/                                                  → marketing redirect (out of scope) or 302 → /signup
/signup                                            → admin SPA: SignupWizard.vue (public)
/t/{tenant}/admin/                                   → admin SPA shell, gated by RequirePortalSession + RequireRole(owner|admin)
/t/{tenant}/admin/api/admin.v1.AdminService/*        → Connect-RPC handlers (this phase)
```

The admin SPA shares the same Pinia store, router base resolution, `@connectrpc/connect-web` transport, and Zitadel OIDC redirect plumbing as the customer portal — Phase 4's cookie at `Path=/t/<tenant>` covers both `/portal/` and `/admin/` subpaths, so a single login serves both UIs. The role interceptor on the admin API rejects calls from `member` sessions; the SPA's router guard mirrors the same check client-side for UX (the server-side check is the only one that matters for security).

The signup wizard at `/signup` runs unauthenticated. After it finishes, it bounces the browser to `/auth/login?tenant=<tenant>&return_to=/t/<tenant>/admin/` so the new owner lands directly in the admin SPA with their first session.

### `proto/limen/admin/v1/admin.proto` — admin API

A second Connect service, separate from `PortalService`. Putting admin RPCs in their own proto keeps the customer-portal codegen surface clean and lets the admin SPA import a smaller, distinct client.

```proto
syntax = "proto3";
package limen.admin.v1;

service AdminService {
  // Public — no session required.
  rpc StartSignup(StartSignupRequest) returns (StartSignupResponse);
  // Authenticated callback after the owner verifies email in Zitadel; finalizes
  // the Tenant row + Zitadel user-grant. See "Self-serve signup" below.
  rpc CompleteSignup(CompleteSignupRequest) returns (CompleteSignupResponse);

  // Authenticated, admin + owner. Upstream catalog CRUD is a Limen-domain
  // operation — Zitadel knows nothing about upstreams.
  rpc CreateUpstream(CreateUpstreamRequest) returns (CreateUpstreamResponse);
  rpc UpdateUpstream(UpdateUpstreamRequest) returns (UpdateUpstreamResponse);
  rpc DeleteUpstream(DeleteUpstreamRequest) returns (DeleteUpstreamResponse);

  // Owner-only. Limen-side tenant lifecycle. Org-level settings (branding,
  // login policy, IdP federation) live in Zitadel Console.
  rpc UpdateTenantSettings(UpdateTenantSettingsRequest) returns (UpdateTenantSettingsResponse);
  rpc DeleteTenant(DeleteTenantRequest) returns (DeleteTenantResponse);          // soft-delete; protected by typed-confirmation
}
```

**Not in this service** (delegated to [Zitadel Console](https://zitadel.com/docs/concepts/features/selfservice)):

- `ListMembers`, `InviteMember`, `ResendInvite`, `UpdateMemberRole`, `RemoveMember` — Console `Users` / `Authorizations` in the tenant's org.
- `TransferOwnership` — same path; tenant admin reassigns the `owner` project role to another user in Console.
- `ListExternalIDPs`, `AddOIDCIDP`, `AddSAMLIDP`, `UpdateExternalIDP`, `RemoveExternalIDP` — Console `Identity Providers` in the tenant's org.
- Profile / password / MFA / passkey RPCs — Console `users/me`.

The admin SPA links to those Console pages instead of routing through Limen. Limen carries no mirror tables, no schema for IdP configs, and no SDK wrappers around Zitadel's IdP / invite APIs.

Requests do **not** carry `tenant_id` for any authenticated method — the tenant `PublicID` comes from `/t/{tenant}/admin/api/...`, exactly as in Phase 9. `StartSignup` and `CompleteSignup` are tenant-agnostic at the URL level and carry their state via a signed token (see below).

### Self-serve signup (`StartSignup` / `CompleteSignup`)

CLI-driven tenant creation in Phase 4 stays for ops / dev / self-hosted installs. The SaaS path is here:

1. **`SignupWizard.vue`** at `/signup` collects: organization name, owner email + name. The page is unauthenticated; a captcha (Cloudflare Turnstile) gates the call. There is no "desired slug" field — Limen mints the tenant `PublicID` (`tnt_<ULID>`) server-side on completion.
2. **`AdminService.StartSignup`** validates inputs (name length, email shape), checks the captcha, and returns a signed signup token `{name, owner_email, owner_name, exp}` HMACed with the Phase 2 encryption key under domain tag `"signup"`. **No Zitadel calls happen yet** — we want abandonment to leave zero side effects.
3. The browser is sent to `/auth/signup?token=<...>`. That handler validates the token, calls Zitadel:
   - `OrganizationService.CreateOrganization` for the new org,
   - `UserService.AddHumanUser` for the owner (Zitadel emails the password-setup link via SMTP / MailHog),
   - `UserService.AddUserGrant(userId, projectId, orgId, ["owner"])`,
   - persists the Limen `Tenant` row (minting a fresh `PublicID = tnt_<ULID>`) with `zitadel_org_id`,
   - mirrors the freshly-minted `PublicID` into the Zitadel org metadata under `limen_tenant_id` (same call as the CLI in Phase 4),
   - sets a one-time `pending_signup` cookie keyed to the new tenant `PublicID`,
   - redirects to a "Check your email" landing page.
4. The owner clicks the email link, sets a password in Zitadel's hosted UI, lands back at `/auth/callback`. Phase 4's callback handler observes the `pending_signup` cookie and calls **`AdminService.CompleteSignup`** which finalizes the `User` row, clears the cookie, and redirects the new owner to `/t/<tenant-public-id>/admin/`.
5. Idempotency: `StartSignup` is a no-op until step 3 fires; `CompleteSignup` is keyed off the `pending_signup` cookie and is idempotent on retry. Errors at step 3 return a generic "could not complete signup".

Rate limits (`internal/resilience`):

- `StartSignup`: per-IP token bucket (5 / hour) + captcha.
- `CompleteSignup`: per-cookie bucket; the cookie is single-shot, so the cap is effectively 1.

### Member management — single source of truth

There is no member-management code in Limen. The Zitadel org owns the membership list, the role grants, and the owner-invariant. Tenant admins manage everything from Console; Limen reads the resulting role on the next ID-token refresh.

### Member management & external IdP federation — link out, don't rebuild

These sit at the top of the admin SPA's sidebar but they are **not** Limen RPCs. The admin shell renders a card-style page that links into the tenant's Zitadel org Console for each operation:

| Card                         | Deep-link target (template)                                      | Console area                          |
| ---------------------------- | ----------------------------------------------------------------- | ------------------------------------- |
| Invite a user                | `<issuer>/ui/console/users?org=<orgId>`                          | Users → New                           |
| Change member role           | `<issuer>/ui/console/users/<userId>/authorizations?org=<orgId>`  | Users → Authorizations                 |
| Remove a user                | `<issuer>/ui/console/users?org=<orgId>`                          | Users                                 |
| Configure SSO / external IdP | `<issuer>/ui/console/org/idp?org=<orgId>`                        | Identity Providers                     |
| Org branding                 | `<issuer>/ui/console/org/branding?org=<orgId>`                   | Branding                              |
| Login / lockout policy       | `<issuer>/ui/console/org/policies/login?org=<orgId>`             | Settings → Login policy               |
| Personal profile / passkeys  | `<issuer>/ui/console/users/me`                                   | User self-service                      |

The SPA fetches `<issuer>` once via `GET /auth/discovery` (a tiny Limen endpoint that returns the static issuer URL from config) and substitutes the tenant's `zitadel_org_id` (carried in the portal cookie's claims as `urn:zitadel:iam:user:resourceowner:id`). The card view is one Vue component, `ZitadelDirectory.vue`, parameterized by the table above — there is no per-card backend work.

This is the [Administrators in delegation](https://zitadel.com/docs/concepts/features/selfservice#administrators-in-delegation) pattern: the tenant's `ORG_OWNER` already has the Zitadel permissions to perform every operation above; reimplementing the UI in Limen would only force Limen to track Zitadel's permission model (which evolves independently) and double-handle every secret on the wire.

### Backend (`internal/admin/`)

```
internal/admin/
├── service.go         // implements AdminServiceHandler
├── interceptor.go     // signup-aware: skips RequirePortalSession on Start/CompleteSignup, enforces it elsewhere
├── signup.go          // StartSignup, CompleteSignup, signed token helpers
├── upstreams_admin.go // Create/Update/DeleteUpstream
├── settings.go        // UpdateTenantSettings, DeleteTenant
└── errors.go          // Connect error mapping
```

Note the absence of `members.go` and `idp.go` — those concerns live in Zitadel.

Mounted with three layered interceptors:

| Interceptor                | Skipped for                     | Enforces                                                 |
| -------------------------- | ------------------------------- | -------------------------------------------------------- |
| `tenancyInterceptor`       | `StartSignup`, `CompleteSignup` | Resolve `{tenant}` → `*Tenant`                             |
| `portalSessionInterceptor` | `StartSignup`, `CompleteSignup` | Decrypt + validate the portal cookie (Phase 4)           |
| `roleInterceptor`          | `StartSignup`, `CompleteSignup` | `owner` for Settings/DeleteTenant; `admin` else          |

The skip-list is annotation-driven (a small per-method table). Unknown methods default to "all interceptors fire" — fail-closed.

### Frontend (`web/src/admin/`)

Same Vue 3 + Vite + Pinia + Vue Router + `@connectrpc/connect-web` stack as Phase 9. The router's top-level bundle code-splits between `portal` and `admin`:

```ts
const routes = [
  { path: "/signup", component: () => import("./admin/SignupWizard.vue") },
  {
    path: "/t/:tenant/portal/:rest*",
    component: () => import("./portal/PortalShell.vue"),
  },
  {
    path: "/t/:tenant/admin/:rest*",
    component: () => import("./admin/AdminShell.vue"),
  },
];
```

Pages:

- `SignupWizard.vue` (public) — name + owner fields, captcha, "Check your email" landing.
- `AdminShell.vue` — top-nav + sidebar; child routes:
  - `Upstreams.vue` (admin scope) — catalog CRUD (this is the **admin** Upstreams page; the per-user link page stays in `/portal/`).
  - `ZitadelDirectory.vue` — the "Manage members & SSO in Zitadel Console" card view described above; one route, no backend.
  - `Settings.vue` — tenant name, `PublicID` (read-only), billing pointer (out of scope for v1), `DeleteTenant`.

The customer-portal SPA gets a small chip in its nav ("Admin →") shown only when the session carries `owner` or `admin` roles; clicking it pushes the user into `/t/<tenant>/admin/`. Same cookie, no re-auth.

### Routing & cookie scope

The Phase 4 portal cookie is set at `Path=/t/<tenant>` (no further suffix), so a single cookie covers both `/portal/` and `/admin/`. We deliberately do **not** issue a separate `/admin`-scoped cookie because:

- The role interceptor is the canonical authorization boundary; cookie scope is not a substitute.
- A single sign-in produces a single session — splitting would force the user to re-authenticate just to walk between two pages of the same tenant.
- Cross-tenant isolation (Phase 4's whole point) is still preserved by `/t/<tenant>` — a tenant-A cookie still cannot leak to tenant B.

The dev Vite proxy and Phase 11's Caddy config gain matching rules to forward `/t/*/admin/api/*` to Limen.

### Build & deploy

- One `pnpm build` produces `web/dist/` containing the three bundles (portal, admin, staff). The static host serves the whole tree.
- `buf generate` produces Go types in `internal/admin/adminv1/` and TS types in `web/src/gen/admin/v1/`.
- `vite.config.ts` adds `/t/*/admin/api/*`, `/auth/signup`, and `/signup` to the dev proxy passthrough list.

## Deliverables

- New `proto/limen/admin/v1/admin.proto` with the `AdminService` definition (signup + upstream catalog CRUD + tenant settings only).
- New `internal/admin/` package (handlers, interceptors, signup, upstream catalog, settings).
- New `web/src/admin/` route module: `SignupWizard.vue`, `AdminShell.vue`, `Upstreams.vue` (admin), `ZitadelDirectory.vue` (deep-link card view), `Settings.vue`.
- Tiny `GET /auth/discovery` endpoint exposing the configured Zitadel issuer URL so the SPA can build Console deep-links without a hard-coded host.
- Updated `web/src/router/index.ts` with lazy-loaded portal / admin / staff bundles.
- Updated Phase 11 Caddyfile + Phase 9 Vite proxy with the new path patterns.
- Updated `AGENTS.md` build section.

**Explicitly _not_ in this phase**: no `tenant_idp_configurations` migration, no Zitadel IdP / invite / member-grant SDK wrappers, no Members or Federation Vue pages, no `internal/admin/members.go` or `internal/admin/idp.go`. Those concerns live in Zitadel Console.

### Migration from Phase 9

The following RPCs originally listed under `PortalService` in [Phase 9](phase-09-portal-spa.md) move to `AdminService` here:

```
CreateUpstream, UpdateUpstream, DeleteUpstream,
UpdateTenantSettings
```

`PortalService` keeps the user-scoped subset: `GetSession`, `ListUpstreams`, `StartConnect`, `SubmitUpstreamAPIKey`, `SetUpstreamLinkEnabled`, `Disconnect`, `ListMCPClients`, `RevokeMCPClient`.

Previously-considered member / IdP / TransferOwnership RPCs are **dropped entirely** — they are not Limen's responsibility (see Phase 4 _Self-service delegation_).

## Security & operational notes

- **No member / role / IdP code in Limen.** Reviewers should reject any PR that adds an `InviteMember`-style RPC, an `AddOIDCIDP`-style wrapper, or an `internal/admin/members.go`. The boundary is in Phase 4's _Self-service delegation_ table.
- **Signup token** is HMAC-signed (Phase 2 key + domain tag `"signup"`) with 30-minute TTL; replay is implicitly limited by the one-shot `pending_signup` cookie set at step 3.
- **Tenant enumeration via signup** is prevented by returning a generic error from `StartSignup` and by the per-IP rate limit + captcha.
- **DeleteTenant** is owner-only, requires typed confirmation of the tenant name, and soft-deletes (`DeletedAt`) — Zitadel org cleanup is a manual operator task documented in the runbook.
- **Console deep-links** point at the configured Zitadel issuer; if a customer ever moves their Zitadel instance, the new issuer URL flows through config, no code change.

## Verification

- Captcha-rejected paths return identical generic errors.
- Signup happy path: `StartSignup` → email lands in MailHog → `/auth/callback` → `CompleteSignup` → new owner lands at `/t/<tenant>/admin/` with `owner` role in their cookie.
- Signup abandonment (token expires unused): zero rows in `tenants`, zero Zitadel orgs created.
- Role isolation: `member` calling any `AdminService` RPC returns `permission_denied`. Admin SPA route guard mirrors this on the client.
- Bundle separation: a `member` user navigating `/t/<tenant>/portal/` from a clean cache pulls the portal bundle only — DevTools network log shows the admin bundle is not fetched.
- `ZitadelDirectory.vue` deep-links resolve to the right Zitadel Console page for the calling user's `orgId` (smoke test against the dev Zitadel container).
- **Delegation guard**: a `grep` in CI fails the build if a Limen file references `InviteMember`, `RemoveMember`, `AddOrgOIDCIDP`, `AddOrgSAMLIDP`, or `tenant_idp_configurations` — those names belong to Zitadel only.

## Risks

- **Captcha vendor lock-in**: hCaptcha vs reCAPTCHA vs Cloudflare Turnstile is a config knob; the SPA reads the chosen provider's site key from `window.__LIMEN_CONFIG__` injected by the static host. Document the matrix in [Phase 11](phase-11-production-deployment.md).
- **Signup → email delivery**: hostile networks may block Zitadel's outbound SMTP. Dev uses MailHog; prod must verify SMTP relay health via the resilience breaker (Phase 10).
- **Two SPAs, one cookie**: a future redesign that needs different idle-timeout policies for admin vs portal would have to split the cookie. Out of scope; flagged here so we don't accidentally hard-code "admin has the same timeout as portal" as an invariant.
- **Zitadel Console URL shape changes** between major versions could break the deep-links in `ZitadelDirectory.vue`. Mitigation: keep the path templates in one Vue constants file, smoke-test them in CI against the dev Zitadel container, and pin a known-good Zitadel image tag in [Phase 0](phase-00-dev-environment.md).

## Checklist

- [ ] `proto/limen/admin/v1/admin.proto` defines `AdminService` with **only** signup + upstream catalog CRUD + tenant-settings RPCs — no member, role, or IdP RPCs
- [ ] `buf generate` produces Go bindings under `internal/admin/adminv1/` and TS under `web/src/gen/admin/v1/`
- [ ] `internal/admin/` package implements the above RPCs and the three layered interceptors with a signup skip-list
- [ ] `internal/admin/` contains **no** `members.go` and **no** `idp.go`; reviewers reject PRs that add them
- [ ] No `tenant_idp_configurations` migration; no `internal/zitadel/` wrappers for `AddOrgOIDCIDP`, `AddOrgSAMLIDP`, `CreateInviteCode`, `AddUserGrant` for non-bootstrap callers, etc.
- [ ] `StartSignup` is captcha-gated and per-IP rate-limited; returns generic errors
- [ ] `CompleteSignup` is keyed off the `pending_signup` cookie and is idempotent
- [ ] Signup completes a full round-trip: name + email → MailHog → password set → admin SPA
- [ ] Admin SPA routes lazy-loaded; `/t/<tenant>/admin/` shell + `Upstreams` + `ZitadelDirectory` + `Settings` pages implemented (no `Members.vue`, no `Federation.vue`)
- [ ] `ZitadelDirectory.vue` renders deep-links for invite / role / remove / IdP / branding / login policy / personal profile, populated with the calling tenant's `orgId`
- [ ] `GET /auth/discovery` returns the configured Zitadel issuer URL for the SPA to build Console deep-links
- [ ] Customer portal SPA shows an "Admin" chip iff the session carries `owner` or `admin`
- [ ] Single portal cookie at `Path=/t/<tenant>` covers both `/portal/` and `/admin/`; role interceptor is the only authorization boundary
- [ ] Vite dev proxy + Phase 11 Caddyfile route `/t/*/admin/api/*`, `/signup`, `/auth/signup`, `/auth/discovery` to Limen
- [ ] Bundle-separation test: a clean-cache `member` browsing `/portal/` does not fetch the admin bundle
- [ ] Phase 9 proto and handlers trimmed: admin RPCs moved out; Phase 9 doc updated to point at this phase
- [ ] Phase 4 _Future work_ item 1 (self-serve signup) marked as delivered by this phase; items previously labelled "member management" and "IdP federation" are recorded as **delegated to Zitadel Console**, not Limen work
- [ ] `AGENTS.md` build section updated
