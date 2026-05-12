# Phase 9b — Tenant administrative portal (Connect-RPC + Vue 3 SPA)

**Depends on**: Phases 4 (portal session, OIDC RP, role claim), 7 (upstream catalog), 9 (shared `web/` codebase, build pipeline, Connect-RPC infrastructure)
**Unblocks**: nothing (sister phase to 9 — together they cover the customer-facing surface)

## Goal

Split the customer-facing web surface into **two SPAs** sharing one `web/` codebase and one build pipeline but mounted on distinct URL paths and lazy-loaded as separate route bundles:

| Phase  | Path                | Audience                       | Bundle            |
| ------ | ------------------- | ------------------------------ | ----------------- |
| **9**  | `/t/{slug}/portal/` | Every authenticated user       | `web/src/portal/` |
| **9b** | `/t/{slug}/admin/`  | Tenant `owner` + `admin` roles | `web/src/admin/`  |
| 12     | `/t/_staff/portal/` | SaaS operator (`super_admin`)  | `web/src/staff/`  |

Phase 9 handles the **user**'s view of their own tenant (link/unlink upstreams, see their MCP clients, profile). Phase 9b handles the **tenant administrator**'s view (members, invitations, role grants, upstream catalog, tenant settings, external IdP federation) plus the **public** self-serve signup flow that bootstraps a brand-new tenant. Phase 12 is the cross-tenant operator backoffice.

Splitting the bundle at the route-loader level means a `member` browsing the customer portal never downloads the admin code, and an unauthenticated visitor to `/signup` never downloads either gated bundle.

## Design

### Where the admin SPA lives

```
/                                                  → marketing redirect (out of scope) or 302 → /signup
/signup                                            → admin SPA: SignupWizard.vue (public)
/t/{slug}/admin/                                   → admin SPA shell, gated by RequirePortalSession + RequireRole(owner|admin)
/t/{slug}/admin/api/admin.v1.AdminService/*        → Connect-RPC handlers (this phase)
```

The admin SPA shares the same Pinia store, router base resolution, `@connectrpc/connect-web` transport, and Zitadel OIDC redirect plumbing as the customer portal — Phase 4's cookie at `Path=/t/<slug>` covers both `/portal/` and `/admin/` subpaths, so a single login serves both UIs. The role interceptor on the admin API rejects calls from `member` sessions; the SPA's router guard mirrors the same check client-side for UX (the server-side check is the only one that matters for security).

The signup wizard at `/signup` runs unauthenticated. After it finishes, it bounces the browser to `/auth/login?tenant=<slug>&return_to=/t/<slug>/admin/` so the new owner lands directly in the admin SPA with their first session.

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

  // Authenticated, admin + owner.
  rpc ListMembers(ListMembersRequest) returns (ListMembersResponse);
  rpc InviteMember(InviteMemberRequest) returns (InviteMemberResponse);          // → Zitadel UserService.AddHumanUser + AddUserGrant + CreateInviteCode
  rpc ResendInvite(ResendInviteRequest) returns (ResendInviteResponse);          // → UserService.ResendInviteCode
  rpc UpdateMemberRole(UpdateMemberRoleRequest) returns (UpdateMemberRoleResponse); // → UserService.UpdateUserGrant
  rpc RemoveMember(RemoveMemberRequest) returns (RemoveMemberResponse);          // → UserService.RemoveUserGrant + DeactivateUser (optional)

  rpc CreateUpstream(CreateUpstreamRequest) returns (CreateUpstreamResponse);
  rpc UpdateUpstream(UpdateUpstreamRequest) returns (UpdateUpstreamResponse);
  rpc DeleteUpstream(DeleteUpstreamRequest) returns (DeleteUpstreamResponse);

  // External IdP federation — drives Zitadel's Management API (see Phase 4
  // future work and "Tenant-level IdP federation" below).
  rpc ListExternalIDPs(ListExternalIDPsRequest) returns (ListExternalIDPsResponse);
  rpc AddOIDCIDP(AddOIDCIDPRequest) returns (AddOIDCIDPResponse);
  rpc AddSAMLIDP(AddSAMLIDPRequest) returns (AddSAMLIDPResponse);
  rpc UpdateExternalIDP(UpdateExternalIDPRequest) returns (UpdateExternalIDPResponse);
  rpc RemoveExternalIDP(RemoveExternalIDPRequest) returns (RemoveExternalIDPResponse);

  // Owner-only.
  rpc UpdateTenantSettings(UpdateTenantSettingsRequest) returns (UpdateTenantSettingsResponse);
  rpc TransferOwnership(TransferOwnershipRequest) returns (TransferOwnershipResponse);
  rpc DeleteTenant(DeleteTenantRequest) returns (DeleteTenantResponse);          // soft-delete; protected by typed-confirmation
}
```

Requests do **not** carry `tenant_id` for any authenticated method — the slug comes from `/t/{slug}/admin/api/...`, exactly as in Phase 9. `StartSignup` and `CompleteSignup` are tenant-agnostic at the URL level and carry their state via a signed token (see below).

### Self-serve signup (`StartSignup` / `CompleteSignup`)

CLI-driven tenant creation in Phase 4 stays for ops / dev / self-hosted installs. The SaaS path is here:

1. **`SignupWizard.vue`** at `/signup` collects: desired slug, organization name, owner email + name. The page is unauthenticated; a captcha (Cloudflare Turnstile) gates the call.
2. **`AdminService.StartSignup`** validates the slug (regex + reserved list — same `ValidateSlug` as Phase 4), checks the captcha, and returns a signed signup token `{slug, name, owner_email, owner_name, exp}` HMACed with the Phase 2 encryption key under domain tag `"signup"`. **No Zitadel calls happen yet** — we don't want to leak existence of a slug, and we want abandonment to leave zero side effects.
3. The browser is sent to `/auth/signup?token=<...>`. That handler validates the token, calls Zitadel:
   - `OrganizationService.CreateOrganization` for the new org,
   - `UserService.AddHumanUser` for the owner (Zitadel emails the password-setup link via SMTP / MailHog),
   - `UserService.AddUserGrant(userId, projectId, orgId, ["owner"])`,
   - persists the Limen `Tenant` row with `zitadel_org_id`,
   - sets a one-time `pending_signup` cookie keyed to the new tenant id,
   - redirects to a "Check your email" landing page.
4. The owner clicks the email link, sets a password in Zitadel's hosted UI, lands back at `/auth/callback`. Phase 4's callback handler observes the `pending_signup` cookie and calls **`AdminService.CompleteSignup`** which finalizes the `User` row, clears the cookie, and redirects the new owner to `/t/<slug>/admin/`.
5. Idempotency: `StartSignup` is a no-op until step 3 fires; `CompleteSignup` is keyed off the `pending_signup` cookie and is idempotent on retry. A duplicate-slug error at step 3 returns a generic "could not complete signup" without revealing whether the slug exists.

Rate limits (`internal/resilience`):

- `StartSignup`: per-IP token bucket (5 / hour) + captcha. Suppresses slug-enumeration.
- `CompleteSignup`: per-cookie bucket; the cookie is single-shot, so the cap is effectively 1.

### Member management — owner invariant in one place

Every grant-mutating RPC (`UpdateMemberRole`, `RemoveMember`, `TransferOwnership`) refuses changes that would leave the tenant with zero `owner` grants. The check is implemented once in `internal/admin/members.go` by:

1. Listing the org's user grants via `UserService.ListUserGrants(orgId, projectId)`.
2. Computing the post-mutation owner count.
3. Aborting with `failed_precondition` and a structured error if the count would drop to zero.

The CLI `invite-user` / `create-tenant` / future `transfer-ownership` subcommands call into the same package so the invariant is enforced exactly once, regardless of caller.

### Tenant-level external IdP federation

Customers running Okta / Auth0 / Entra ID / Google Workspace / a generic OIDC or SAML IdP can plug it into their Zitadel org so their users sign in against their own corporate IdP. Limen does **no** federation itself — it drives Zitadel's Management API and stores a thin mirror row for visibility.

- **Storage**: a new `tenant_idp_configurations` table (`tenant_id`, `zitadel_idp_id`, `kind`, `name`, `enabled`, audit columns). No secrets stored — Zitadel holds the issuer / client / signing cert.
- **RPCs**:
  - `AddOIDCIDP` → `ManagementService.AddOrgOIDCIDP` (issuer URL, client id / secret, scopes, attribute mapping).
  - `AddSAMLIDP` → `ManagementService.AddOrgSAMLIDP` (metadata URL or XML, binding, attribute mapping).
  - `UpdateExternalIDP` / `RemoveExternalIDP` — straight passthrough.
- **Provisioning model**: JIT user provisioning + attribute mapping are configured Zitadel-side per IdP. SCIM is a Zitadel feature; if a customer needs it we point them at Zitadel's SCIM docs.
- **Login surface**: once an IdP is enabled on the org, Zitadel's hosted login page automatically renders the "Sign in with Okta / Entra / ..." button. Limen's `/auth/login` flow is unchanged.

This is the realization of the "Tenant-level external IdP federation" item from Phase 4's _Future work_ section.

### Backend (`internal/admin/`)

```
internal/admin/
├── service.go         // implements AdminServiceHandler
├── interceptor.go     // signup-aware: skips RequirePortalSession on Start/CompleteSignup, enforces it elsewhere
├── signup.go          // StartSignup, CompleteSignup, signed token helpers
├── members.go         // shared owner-invariant + grant CRUD (called from CLI too)
├── upstreams_admin.go // Create/Update/DeleteUpstream
├── idp.go             // external IdP federation
├── settings.go        // UpdateTenantSettings, TransferOwnership, DeleteTenant
└── errors.go          // Connect error mapping
```

Mounted with three layered interceptors:

| Interceptor                | Skipped for                     | Enforces                                                 |
| -------------------------- | ------------------------------- | -------------------------------------------------------- |
| `tenancyInterceptor`       | `StartSignup`, `CompleteSignup` | Resolve `{slug}` → `*Tenant`                             |
| `portalSessionInterceptor` | `StartSignup`, `CompleteSignup` | Decrypt + validate the portal cookie (Phase 4)           |
| `roleInterceptor`          | `StartSignup`, `CompleteSignup` | `owner` for Settings/Transfer/DeleteTenant; `admin` else |

The skip-list is annotation-driven (a small per-method table). Unknown methods default to "all interceptors fire" — fail-closed.

### Frontend (`web/src/admin/`)

Same Vue 3 + Vite + Pinia + Vue Router + `@connectrpc/connect-web` stack as Phase 9. The router's top-level bundle code-splits between `portal` and `admin`:

```ts
const routes = [
  { path: "/signup", component: () => import("./admin/SignupWizard.vue") },
  {
    path: "/t/:slug/portal/:rest*",
    component: () => import("./portal/PortalShell.vue"),
  },
  {
    path: "/t/:slug/admin/:rest*",
    component: () => import("./admin/AdminShell.vue"),
  },
];
```

Pages:

- `SignupWizard.vue` (public) — slug + name + owner fields, captcha, "Check your email" landing.
- `AdminShell.vue` — top-nav + sidebar; child routes:
  - `Members.vue` — list + invite + role change + remove; surfaces owner-invariant errors inline.
  - `Upstreams.vue` (admin scope) — catalog CRUD (this is the **admin** Upstreams page; the per-user link page stays in `/portal/`).
  - `Federation.vue` — list + add / edit / remove external IdPs.
  - `Settings.vue` — tenant name, slug (read-only), billing pointer (out of scope for v1), `TransferOwnership`, `DeleteTenant`.

The customer-portal SPA gets a small chip in its nav ("Admin →") shown only when the session carries `owner` or `admin` roles; clicking it pushes the user into `/t/<slug>/admin/`. Same cookie, no re-auth.

### Routing & cookie scope

The Phase 4 portal cookie is set at `Path=/t/<slug>` (no further suffix), so a single cookie covers both `/portal/` and `/admin/`. We deliberately do **not** issue a separate `/admin`-scoped cookie because:

- The role interceptor is the canonical authorization boundary; cookie scope is not a substitute.
- A single sign-in produces a single session — splitting would force the user to re-authenticate just to walk between two pages of the same tenant.
- Cross-tenant isolation (Phase 4's whole point) is still preserved by `/t/<slug>` — a tenant-A cookie still cannot leak to tenant B.

The dev Vite proxy and Phase 11's Caddy config gain matching rules to forward `/t/*/admin/api/*` to Limen.

### Build & deploy

- One `pnpm build` produces `web/dist/` containing the three bundles (portal, admin, staff). The static host serves the whole tree.
- `buf generate` produces Go types in `internal/admin/adminv1/` and TS types in `web/src/gen/admin/v1/`.
- `vite.config.ts` adds `/t/*/admin/api/*`, `/auth/signup`, and `/signup` to the dev proxy passthrough list.

## Deliverables

- New `proto/limen/admin/v1/admin.proto` with the `AdminService` definition.
- New `internal/admin/` package (handlers, interceptors, signup, member invariant, external IdP wrappers).
- Updated `internal/zitadel/` wrappers: thin `AddOrgOIDCIDP`, `AddOrgSAMLIDP`, `ListOrgIDPs`, `UpdateOrgIDP`, `RemoveOrgIDP` helpers on top of the SDK Management service.
- New migration (goose, see `internal/storage/MIGRATIONS.md`): `tenant_idp_configurations` table.
- New `web/src/admin/` route module: `SignupWizard.vue`, `AdminShell.vue`, `Members.vue`, `Upstreams.vue` (admin), `Federation.vue`, `Settings.vue`.
- Updated `web/src/router/index.ts` with lazy-loaded portal / admin / staff bundles.
- Updated Phase 9 `PortalService` proto (admin RPCs moved out — see "Migration from Phase 9" below).
- Updated Phase 11 Caddyfile + Phase 9 Vite proxy with the new path patterns.
- Updated `AGENTS.md` build section.

### Migration from Phase 9

The following RPCs originally listed under `PortalService` in [Phase 9](phase-09-portal-spa.md) move to `AdminService` here:

```
CreateUpstream, UpdateUpstream, DeleteUpstream,
ListMembers, InviteMember, ResendInvite, UpdateMemberRole, RemoveMember,
UpdateTenantSettings, TransferOwnership
```

`PortalService` keeps the user-scoped subset: `GetSession`, `ListUpstreams`, `StartConnect`, `SubmitUpstreamAPIKey`, `SetUpstreamLinkEnabled`, `Disconnect`, `ListMCPClients`, `RevokeMCPClient`.

## Security & operational notes

- **Owner invariant** is enforced server-side in `internal/admin/members.go` and reused by the Phase 4 CLI — there is exactly one implementation.
- **Signup token** is HMAC-signed (Phase 2 key + domain tag `"signup"`) with 30-minute TTL; replay is implicitly limited by the one-shot `pending_signup` cookie set at step 3.
- **Slug enumeration via signup** is prevented by returning a generic error from `StartSignup` and by the per-IP rate limit + captcha.
- **Federation secrets** never reach Limen — OIDC client secrets and SAML signing certs are POSTed to Zitadel directly through the SDK and Limen stores only the Zitadel `idp_id` for cross-reference.
- **External IdP misconfiguration** locking the tenant out: documented in the runbook ([Phase 10](phase-10-wiring-hardening.md)) — staff (Phase 12) can force-disable an IdP from the backoffice.
- **DeleteTenant** is owner-only, requires typed confirmation of the slug, and soft-deletes (`DeletedAt`) — Zitadel org cleanup is a manual operator task documented in the runbook.

## Verification

- Slug + captcha rejected paths return identical generic errors regardless of slug existence.
- Signup happy path: `StartSignup` → email lands in MailHog → `/auth/callback` → `CompleteSignup` → new owner lands at `/t/<slug>/admin/` with `owner` role in their cookie.
- Signup abandonment (token expires unused): zero rows in `tenants`, zero Zitadel orgs created.
- Owner invariant: `UpdateMemberRole(owner→admin)` on the sole owner returns `failed_precondition`; CLI call site behaves identically.
- External IdP add → Zitadel org reflects the new IdP → Limen mirror row appears → Zitadel hosted login renders the new SSO button.
- Role isolation: `member` calling any `AdminService` RPC returns `permission_denied`. Admin SPA route guard mirrors this on the client.
- Bundle separation: a `member` user navigating `/t/<slug>/portal/` from a clean cache pulls the portal bundle only — DevTools network log shows the admin bundle is not fetched.

## Risks

- **Captcha vendor lock-in**: hCaptcha vs reCAPTCHA vs Cloudflare Turnstile is a config knob; the SPA reads the chosen provider's site key from `window.__LIMEN_CONFIG__` injected by the static host. Document the matrix in [Phase 11](phase-11-production-deployment.md).
- **Signup → email delivery**: hostile networks may block Zitadel's outbound SMTP. Dev uses MailHog; prod must verify SMTP relay health via the resilience breaker (Phase 10).
- **Two SPAs, one cookie**: a future redesign that needs different idle-timeout policies for admin vs portal would have to split the cookie. Out of scope; flagged here so we don't accidentally hard-code "admin has the same timeout as portal" as an invariant.
- **External IdP attribute mapping** drift between Zitadel versions can break JIT provisioning silently. Mitigation: a once-a-day staff smoke check (Phase 12) that surfaces tenants with `external_idp.enabled=true` but `last_successful_login_via_idp > 7 days`.

## Checklist

- [ ] `proto/limen/admin/v1/admin.proto` defines `AdminService` with the RPCs listed above
- [ ] `buf generate` produces Go bindings under `internal/admin/adminv1/` and TS under `web/src/gen/admin/v1/`
- [ ] `internal/admin/` package implements all RPCs and the three layered interceptors with a signup skip-list
- [ ] `internal/admin/members.go` enforces the at-least-one-owner invariant in **one** place; the Phase 4 CLI calls into it
- [ ] `tenant_idp_configurations` goose migration shipped
- [ ] `internal/zitadel/` adds typed wrappers for `AddOrgOIDCIDP`, `AddOrgSAMLIDP`, list/update/remove IdP
- [ ] `StartSignup` is captcha-gated and per-IP rate-limited; returns generic errors
- [ ] `CompleteSignup` is keyed off the `pending_signup` cookie and is idempotent
- [ ] Signup completes a full round-trip: slug + email → MailHog → password set → admin SPA
- [ ] Admin SPA routes lazy-loaded; `/t/<slug>/admin/` shell + Members + Upstreams (admin) + Federation + Settings pages implemented
- [ ] Customer portal SPA shows an "Admin" chip iff the session carries `owner` or `admin`
- [ ] Single portal cookie at `Path=/t/<slug>` covers both `/portal/` and `/admin/`; role interceptor is the only authorization boundary
- [ ] Vite dev proxy + Phase 11 Caddyfile route `/t/*/admin/api/*`, `/signup`, `/auth/signup` to Limen
- [ ] Bundle-separation test: a clean-cache `member` browsing `/portal/` does not fetch the admin bundle
- [ ] Phase 9 proto and handlers trimmed: admin RPCs moved out; Phase 9 doc updated to point at this phase
- [ ] Phase 4 _Future work_ items 1 (self-serve signup) and 3 (external IdP federation) marked as delivered by this phase
- [ ] `AGENTS.md` build section updated; runbook entry for "IdP lock-out recovery" added in [Phase 10](phase-10-wiring-hardening.md)
