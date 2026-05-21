# Phase 9c — Tenant administrative portal (Connect-RPC + Vue 3 SPA)

**Depends on**: Phases 4 (portal session, OIDC RP, role claim), 7 (upstream catalog), 9 (shared `web/` codebase, build pipeline, Connect-RPC infrastructure)
**Unblocks**: nothing (sister phase to 9 — together they cover the customer-facing surface)

## Goal

Split the customer-facing web surface into **two SPAs** sharing one `web/` codebase and one build pipeline but mounted on distinct URL paths and lazy-loaded as separate route bundles:

| Phase  | Path                  | Audience                       | Bundle            |
| ------ | --------------------- | ------------------------------ | ----------------- |
| **9**  | `/t/{tenant}/portal/` | Every authenticated user       | `web/src/portal/` |
| **9b** | `/t/{tenant}/admin/`  | Tenant `owner` + `admin` roles | `web/src/admin/`  |
| 12     | `/t/_staff/portal/`   | SaaS operator (`super_admin`)  | `web/src/staff/`  |

Phase 9b handles the **user**'s view of their own tenant (link/unlink upstreams, see their MCP clients, profile). Phase 9c handles the **tenant administrator**'s view of the **Limen-domain** admin surface: an **onboarding dashboard**, upstream catalog CRUD, member management (phased — see below), tenant settings, and the public self-serve signup flow that bootstraps a brand-new tenant.

**Member management is phased across three releases.** v1 (this phase) renders a deep-link card to [Zitadel Console](https://zitadel.com/docs/concepts/features/selfservice). v1.5 adds a read-only `ListMembers` RPC that proxies Zitadel's Management API and surfaces an inline members table; mutations still bounce to Console. v2 takes full ownership with `InviteMember` / `UpdateMemberRole` / `RemoveMember` RPCs backed by a thin `internal/zitadel/management.go` client. The phasing is summarised in the [_Member management — phased delivery_](#member-management--phased-delivery) section below and reflected verbatim in the _Self-service delegation_ table in [Phase 4](phase-04-tenant-auth-session.md).

**External IdP federation, password / MFA / passkey enrollment, and login / lockout policy stay in Zitadel Console permanently.** Those concerns evolve with Zitadel's permission model; reimplementing them in Limen would force us to track that model and double-handle every secret on the wire. The admin SPA renders deep-link cards for each of them — there is no plan to ever own that surface.

Splitting the bundle at the route-loader level means a `member` browsing the customer portal never downloads the admin code, and an unauthenticated visitor to `/signup` never downloads either gated bundle.

The admin SPA's visual language — sidebar structure, topbar layout, design tokens, theming (light + dark), component vocabulary, and the Lucide icon mapping — is normative in [`docs/frontend-design.md`](../frontend-design.md). This phase implements that spec; it does not redefine it.

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

  // Force a re-index of an upstream's tool catalog. For per-user strategies
  // (mcp_spec, static_header user-mode) the caller must already hold an
  // enabled link to the upstream — see "Tool catalog bootstrap" below.
  rpc ReindexUpstreamCatalog(ReindexUpstreamCatalogRequest) returns (ReindexUpstreamCatalogResponse);

  // Owner-only. Limen-side tenant lifecycle. Org-level settings (branding,
  // login policy, IdP federation) live in Zitadel Console.
  rpc UpdateTenantSettings(UpdateTenantSettingsRequest) returns (UpdateTenantSettingsResponse);
  rpc DeleteTenant(DeleteTenantRequest) returns (DeleteTenantResponse);          // soft-delete; protected by typed-confirmation
}
```

**Not in this v1 service** (delegated to [Zitadel Console](https://zitadel.com/docs/concepts/features/selfservice) or scheduled for v1.5/v2):

| RPC family                                                                               | v1 (this phase)          | v1.5                                            | v2                                              |
| ---------------------------------------------------------------------------------------- | ------------------------ | ----------------------------------------------- | ----------------------------------------------- |
| `ListMembers`                                                                            | deep-link to Console     | **read-only Management-API proxy** (added here) | unchanged                                       |
| `InviteMember`, `UpdateMemberRole`, `RemoveMember`, `ResendInvite`                       | deep-link to Console     | deep-link to Console                            | **added** (writes via Zitadel Management API)   |
| `TransferOwnership`                                                                      | deep-link to Console     | deep-link to Console                            | deep-link to Console (deliberately kept manual) |
| `ListExternalIDPs`, `AddOIDCIDP`, `AddSAMLIDP`, `UpdateExternalIDP`, `RemoveExternalIDP` | **permanent delegation** | permanent delegation                            | permanent delegation                            |
| Profile / password / MFA / passkey                                                       | **permanent delegation** | permanent delegation                            | permanent delegation                            |

Limen carries no mirror tables for membership in any version — `ListMembers` proxies in v1.5 and the v2 mutations write straight through to Zitadel. No schema for IdP configs is added at any version.

Requests do **not** carry `tenant_id` for any authenticated method — the tenant `PublicID` comes from `/t/{tenant}/admin/api/...`, exactly as in Phase 9b. `StartSignup` and `CompleteSignup` are tenant-agnostic at the URL level and carry their state via a signed token (see below).

### Onboarding dashboard (v1) — Limen's first impression

The `/t/{tenant}/admin/` index route is **`Dashboard.vue`**, modelled on the Stitch "Onboarding Dashboard" screen. It is intentionally task-focused, not metric-focused, because in the user's first session there is nothing meaningful to chart yet.

| Section              | v1 (this phase)                                                                                                                                                                                                                     | Later phase                                                                                                                                                                                                    |
| -------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Welcome header       | "Welcome to Limen, `{firstName}`" + one-sentence onboarding sub.                                                                                                                                                                    | unchanged                                                                                                                                                                                                      |
| Setup Progress card  | Progress bar + "X of N steps completed" + percent. N=3 in v1.                                                                                                                                                                       | unchanged                                                                                                                                                                                                      |
| Task bento           | Primary card **Connect MCP Servers** → `/mcp-servers/new`. Secondary stack: **Invite Your Team** → opens `ZitadelDirectory.vue` panel (v1) / scrolls to inline members table (v1.5+). **Configure Organization** → `/org/settings`. | Adds a fourth tile when v1.5/v2 milestones land (e.g. **Review Audit Log**, **Inspect Active Sessions**).                                                                                                      |
| System Health card   | Empty state ("Waiting for data…") with a `Monitor` icon and a one-sentence explanation.                                                                                                                                             | Replace with a 3-card metric tile row: Active MCP Servers `N / N_max`, Connected Clients, P95 Latency (sourced from MCP RS metrics — see the Stitch "Admin Dashboard (Updated Nav)" reference for the visual). |
| Quick Resources list | Documentation / API Reference / Community Support deep-links to public docs.                                                                                                                                                        | unchanged                                                                                                                                                                                                      |

**Step completion rules** (computed client-side from `ListUpstreams` + tenant-settings state — no dedicated RPC):

1. _Connect MCP Servers_ — checked when at least one row in `ListUpstreams` has `status == ready` (i.e. `tool_count > 0`).
2. _Invite Your Team_ — checked when `tenant_settings.invited_team_at IS NOT NULL`. A one-shot flag the SPA flips when the user first opens the deep-link card / members table; this is a nudge, not verification — the actual invite happens in Console (v1) or via the v2 RPC.
3. _Configure Organization_ — checked when `tenant_settings.configured_at IS NOT NULL`, set by the first successful `UpdateTenantSettings` call.

The two timestamp fields live on the existing `tenant_settings` JSONB column added in Phase 4 — no new schema. The SPA writes them through the existing `UpdateTenantSettings` RPC.

### Contextual search — two modes

The topbar's search input is one component (`ContextualSearch.vue`) with two behaviours selected by `route.meta.search`:

| Mode      | When                                                                           | Behaviour                                                                                                                                                 |
| --------- | ------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `filter`  | List / detail routes (e.g. `/mcp-servers`, `/mcp-servers/:id`, `/org/members`) | Debounced 200 ms; filters the page's local list / table in-place. Empty input → full list. No global jump.                                                |
| `palette` | Dashboard (`/`) and any future overview routes                                 | `/` keyboard shortcut opens a modal-style palette listing upstreams + members (v1.5+) + settings sections, all client-side. Selecting a result navigates. |
| _(none)_  | `/org/settings`                                                                | Topbar hides the input — settings has no list to filter and the palette would duplicate the sidebar.                                                      |

The placeholder text is also route-driven (`route.meta.search.placeholder`). Per-page filter rows (e.g. the role dropdown on members) live on the page itself, not in the topbar.

### Tool catalog bootstrap (admin-driven for OAuth upstreams)

Every upstream surfaces tools to users via the per-upstream `UpstreamTool` catalog populated by [Phase 8](phase-08-per-tenant-injection.md)'s `IndexUpstream`. _Who_ triggers the first index depends on the strategy:

| Strategy                                | Bootstrap path                                                                                                                                                                                                                                                                                                       |
| --------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `none`                                  | `CreateUpstream` runs `IndexUpstream` synchronously and returns the populated catalog in the response. No user action.                                                                                                                                                                                               |
| `static_header` (tenant-wide mode)      | Same as `none`: the tenant-wide secret is in `UpstreamStrategyConfig`, so the indexer runs immediately.                                                                                                                                                                                                              |
| `mcp_spec`, `static_header` (user mode) | `CreateUpstream` provisions the upstream in a `pending_catalog` state; the admin SPA then walks the caller through the standard portal connect flow as the **mandatory next step**. Until at least one `owner` or `admin` completes that flow, the upstream is hidden from other tenant users and `tool_count` is 0. |

UI shape in `Upstreams.vue`:

1. "New upstream" form collects strategy + URL (+ optional static OAuth client for mcp_spec ASes without DCR). On submit, `CreateUpstream` runs and returns `{upstream, requires_admin_link: bool, connect_url: string}`.
2. If `requires_admin_link` is true the SPA opens a modal: "Connect your account to finish setup. Tools will not be available to your team until you complete this step." with a primary button that POSTs `PortalService.StartConnect` for the just-created upstream (same RPC the per-user portal uses) and redirects the browser to the upstream's authorization URL.
3. After the round-trip lands on `/auth/callback`, Phase 7's `Service.FinishCallback` calls `IndexUpstream` because the linking user holds `owner`/`admin` (Phase 8 enforces the role gate). The SPA's Upstreams list polls `ListUpstreams` and flips the row from `pending_catalog` → `ready` once `tool_count > 0`.
4. `ReindexUpstreamCatalog` is the manual escape hatch (e.g. the upstream added a tool out-of-band before the next refresher sweep). For per-user strategies it runs under the caller's link; if the caller has no enabled link the RPC returns `failed_precondition` with a message telling them to connect first. For tenant-mode strategies it runs unconditionally.

The SPA never tries to bootstrap a catalog under a `member`'s credentials — the bootstrap is an admin responsibility precisely because the resulting catalog is shared across every user of the tenant.

### Per-upstream ambient context editor ([Phase 8c](phase-08c-ambient-context-and-alias-discovery.md))

The admin Upstreams page edits the **upstream-level** `defaults_json` JSONB blob introduced in Phase 8c (the per-link `context_json` is edited in the user's portal in Phase 9b). Both surfaces share the validation rules below, so the UX is documented here once and referenced from the portal phase.

The blob is exposed to codemode scripts as `codemode.tools().upstreams[i].context` (see [Phase 8c](phase-08c-ambient-context-and-alias-discovery.md) for the full envelope shape) — there is no server-side injection into tool calls — so the editor's job is to make the blob easy to write correctly and impossible to write nonsensically.

Component shape on the upstream detail page in `Upstreams.vue`:

| Element                  | Behavior                                                                                                                                                                                                                                                                                                                                                                       |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| JSON editor              | Monaco editor (already a Vite-friendly bundle) configured with `language: "json"`, `formatOnPaste`, `formatOnType`, and tab size 2. Diagnostics are surfaced inline by Monaco's built-in JSON worker.                                                                                                                                                                          |
| Live validation          | A computed property re-parses on every keystroke (debounced 200 ms). Surfaces three states under the editor: `Valid · 142 B`, `Invalid JSON: <message>`, or `Too large: 4321 B > 4096 B`. The "Save" button is disabled in the latter two.                                                                                                                                     |
| Schema hints             | A small static map per known strategy / upstream URL pattern offers suggested key names as ghost-text completions: `cloudId` for Atlassian (`api.atlassian.com`), `organization_slug` for Sentry (`*.sentry.io`), `account_id` for Cloudflare (`api.cloudflare.com`). Suggestions only — never enforced. The map lives in `web/src/admin/upstreamContextHints.ts`.             |
| Reset button             | "Reset to empty" writes `{}` and prompts a confirm dialog if the current blob is non-empty. Convenient for fixing autopopulated values that are wrong; explicit because users will reach for it accidentally.                                                                                                                                                                  |
| Save                     | Calls `AdminService.UpdateUpstream` with the new `defaults_json` field. On `invalid_argument` from the server, the editor highlights the offending field path (returned in the Connect error's `details`) and surfaces the server message above the editor. Server-side re-validation is mandatory — the client check is a UX accelerator, not a security boundary.            |
| Read-only "merged" panel | Below the editor, a collapsed `<details>` shows what the matching group on `codemode.tools()` will surface for a representative user: `merge(linkContext, defaultsJson)`. Lets the admin spot-check that their defaults won't be silently overridden. Per-link context is fetched on demand for a chosen user (admin-only RPC `AdminService.PreviewUpstreamContext(user_id)`). |

Validation rules enforced **both** client-side (for UX) and server-side (for safety), as specified in Phase 8c:

- Top-level value must be a JSON **object** — `[]`, scalars, and `null` are rejected.
- Serialized size ≤ 4 KB.
- Top-level keys match `^[A-Za-z_$][\w$]*$` so the model can spread them as `{...up.context}` without bracket-notation tricks.

The same component is reused in the customer portal ([Phase 9b](phase-09b-portal-spa.md)) for the per-link `context_json` editor; only the RPC backing it changes (`PortalService.UpdateUpstreamLinkContext`). The portal phase imports `web/src/components/ContextJsonEditor.vue` and re-exports the same hint map.

`AdminService.UpdateUpstream` calls the shared `gateway.validateContextBlob` helper from [Phase 8c](phase-08c-ambient-context-and-alias-discovery.md) on every write. Validation errors are mapped to Connect `invalid_argument` with a structured `field_path` detail (`{"path":"defaults_json","reason":"root_not_object"}`) so the SPA can highlight precisely.

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

### Member management — phased delivery

Membership data lives in Zitadel at every phase. Limen never holds a mirror copy. What changes is **how the admin SPA surfaces it**:

#### v1 — deep-link only (this phase)

`Members.vue` renders `ZitadelDirectory.vue`, a card-style page that links into the tenant's Zitadel org Console for each operation. No Limen RPCs are involved.

| Card                         | Deep-link target (template)                                     | Console area            |
| ---------------------------- | --------------------------------------------------------------- | ----------------------- |
| Invite a user                | `<issuer>/ui/console/users?org=<orgId>`                         | Users → New             |
| Change member role           | `<issuer>/ui/console/users/<userId>/authorizations?org=<orgId>` | Users → Authorizations  |
| Remove a user                | `<issuer>/ui/console/users?org=<orgId>`                         | Users                   |
| Configure SSO / external IdP | `<issuer>/ui/console/org/idp?org=<orgId>`                       | Identity Providers      |
| Org branding                 | `<issuer>/ui/console/org/branding?org=<orgId>`                  | Branding                |
| Login / lockout policy       | `<issuer>/ui/console/org/policies/login?org=<orgId>`            | Settings → Login policy |
| Personal profile / passkeys  | `<issuer>/ui/console/users/me`                                  | User self-service       |

The SPA fetches `<issuer>` once via `GET /auth/discovery` (a tiny Limen endpoint that returns the static issuer URL from config) and substitutes the tenant's `zitadel_org_id` (carried in the portal cookie's claims as `urn:zitadel:iam:user:resourceowner:id`).

This is the [Administrators in delegation](https://zitadel.com/docs/concepts/features/selfservice#administrators-in-delegation) pattern: the tenant's `ORG_OWNER` already has the Zitadel permissions to perform every operation above.

#### v1.5 — read-only members table

A new `AdminService.ListMembers` RPC proxies Zitadel's `ManagementService.ListUsers` (filtered by the tenant's `zitadel_org_id`) and returns name, email, role, status, last login. `Members.vue` swaps from the deep-link card grid to the full table layout from the Stitch "Users & Roles" reference: search input + role dropdown filter + table (avatar / name / email / role / status / last login / actions). The **Actions** column items still bounce to Console for invite / role / remove via the same deep-link templates above — only the visualisation changed. The IdP / branding / login-policy / profile cards stay deep-linked permanently and move to a collapsed "Identity & policies" panel beneath the members table.

A new thin `internal/zitadel/management.go` client is introduced here. It uses Zitadel's machine-user JWT (the same one bootstrap uses) and is the **only** way Limen talks to Zitadel's Management API; it returns Limen-shaped DTOs so the admin handler stays free of Zitadel proto types.

#### v2 — full ownership of writes

`InviteMember`, `UpdateMemberRole`, `RemoveMember`, `ResendInvite` are added to `AdminService`. They write through `internal/zitadel/management.go` to Zitadel's Management API. `Members.vue` swaps its action handlers from `window.open(deepLink)` to direct RPC calls; the IdP / branding / login-policy / profile cards remain deep-linked permanently.

`TransferOwnership` stays manual at every version (deep-link to Console). Reassigning the `owner` project role is rare, high-impact, and not worth a custom UI.

### Backend (`internal/admin/`)

```
internal/admin/
├── service.go         // implements AdminServiceHandler
├── interceptor.go     // signup-aware: skips RequirePortalSession on Start/CompleteSignup, enforces it elsewhere
├── signup.go          // StartSignup, CompleteSignup, signed token helpers
├── upstreams_admin.go // Create/Update/DeleteUpstream, ReindexUpstreamCatalog
├── settings.go        // UpdateTenantSettings, DeleteTenant
└── errors.go          // Connect error mapping
```

v1 ships **no** `members.go` and **no** `idp.go`. v1.5 introduces `members.go` (read-only `ListMembers`) and `internal/zitadel/management.go` (the thin Management-API client). v2 expands `members.go` with `InviteMember` / `UpdateMemberRole` / `RemoveMember` / `ResendInvite`. `idp.go` and any `tenant_idp_configurations` migration never appear — IdP federation is permanently delegated.

Mounted with three layered interceptors:

| Interceptor                | Skipped for                     | Enforces                                        |
| -------------------------- | ------------------------------- | ----------------------------------------------- |
| `tenancyInterceptor`       | `StartSignup`, `CompleteSignup` | Resolve `{tenant}` → `*Tenant`                  |
| `portalSessionInterceptor` | `StartSignup`, `CompleteSignup` | Decrypt + validate the portal cookie (Phase 4)  |
| `roleInterceptor`          | `StartSignup`, `CompleteSignup` | `owner` for Settings/DeleteTenant; `admin` else |

The skip-list is annotation-driven (a small per-method table). Unknown methods default to "all interceptors fire" — fail-closed.

### Frontend (`web/src/admin/`)

Same Vue 3 + Vite + Pinia + Vue Router + `@connectrpc/connect-web` stack as Phase 9b. The router's top-level bundle code-splits between `portal` and `admin`:

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

Pages (v1 — this phase):

- `SignupWizard.vue` (public) — name + owner fields, captcha, "Check your email" landing.
- `AdminShell.vue` — `Sidebar` + `Topbar` + `<RouterView/>`. Sidebar structure, topbar layout, and dark-mode theming are defined in [`docs/frontend-design.md`](../frontend-design.md). The topbar hosts `ContextualSearch.vue` (two-mode component documented above) and the identity dropdown.
- Child routes under `/t/{tenant}/admin/`:

| Route              | Component            | Search mode      | Notes                                                                                                  |
| ------------------ | -------------------- | ---------------- | ------------------------------------------------------------------------------------------------------ |
| `/`                | `Dashboard.vue`      | `palette`        | Welcome + setup-progress + bento + system-health empty state.                                          |
| `/mcp-servers`     | `Upstreams.vue`      | `filter`         | Catalog list (the **admin** Upstreams page; the per-user link page stays in `/portal/`).               |
| `/mcp-servers/new` | `UpstreamWizard.vue` | _(none)_         | Modal-in-page wizard with 3 strategy variants (Anonymous / OAuth / MCP Spec).                          |
| `/mcp-servers/:id` | `UpstreamDetail.vue` | `filter`         | Strategy, tool catalog, `ContextJsonEditor.vue` for `defaults_json`, reindex, danger zone.             |
| `/org/members`     | `Members.vue`        | `filter` (v1.5+) | v1 renders `ZitadelDirectory.vue`; v1.5 renders inline table via `ListMembers`; v2 adds write actions. |
| `/org/settings`    | `Settings.vue`       | _(none)_         | Owner-only Limen fields **and** read-only Zitadel deep-link panel (see below).                         |

`Settings.vue` covers **both** the Limen-owned tenant fields and a read-only mirror of the key Zitadel-side identity facts, matching the Stitch "Organization Settings (Read-only)" reference:

- **Organization Profile** — tenant name (editable), `PublicID` (read-only display), primary domain (read-only display from config).
- **Identity & Access Management** — read-only panel sourced from `/auth/discovery` + the cookie's claims: "Connected" pill, SSO Active row, MFA Required row, Zitadel domain, Organization ID, and a "Manage in Zitadel Console" deep-link. No editable fields — this panel is a confidence-builder, not a config surface.
- **DCR redirect-URI allowlist editor** — manages the full list of glob patterns per [Phase 5](phase-05-authorization-server.md): add / edit / remove individual entries, with client-side and server-side validation against the shared `internal/oauthproxy/uripolicy.go` matcher; duplicates deduped on save.
- **Danger Zone** — `DeleteTenant` (owner-only, typed-confirmation, soft-delete).
- Billing pointer (out of scope for v1, slot-only until Phase 13).

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

v1 (this phase):

- New `proto/limen/admin/v1/admin.proto` with the `AdminService` definition (signup + upstream catalog CRUD + tenant settings only — no member RPCs at v1).
- New `internal/admin/` package (handlers, interceptors, signup, upstream catalog, settings). No `members.go`, no `idp.go`.
- New `web/src/admin/` route module: `SignupWizard.vue`, `AdminShell.vue`, `Sidebar.vue`, `Topbar.vue`, `ContextualSearch.vue`, `Dashboard.vue`, `Upstreams.vue`, `UpstreamWizard.vue`, `UpstreamDetail.vue`, `Members.vue` (wraps `ZitadelDirectory.vue` at v1), `Settings.vue`.
- Shared `web/src/components/ContextJsonEditor.vue` and `web/src/admin/upstreamContextHints.ts`, re-used by Phase 9b.
- Tiny `GET /auth/discovery` endpoint exposing the configured Zitadel issuer URL so the SPA can build Console deep-links without a hard-coded host.
- Updated `web/src/router/index.ts` with lazy-loaded portal / admin / staff bundles.
- Updated Phase 11 Caddyfile + Phase 9b Vite proxy with the new path patterns.
- Updated `AGENTS.md` build section.

v1.5 (follow-up):

- Add `ListMembers` to `proto/limen/admin/v1/admin.proto`.
- Add `internal/admin/members.go` (read-only) and `internal/zitadel/management.go` (thin Management-API client).
- Swap `Members.vue` from `ZitadelDirectory.vue` to the inline table layout.

v2 (follow-up):

- Add `InviteMember`, `UpdateMemberRole`, `RemoveMember`, `ResendInvite` to the proto + handler.
- Wire `Members.vue` actions to the new RPCs.

**Explicitly _not_ ever in Limen**: no `tenant_idp_configurations` migration, no Zitadel IdP wrappers (`AddOrgOIDCIDP`, `AddOrgSAMLIDP`, etc.), no `internal/admin/idp.go`, no profile / password / MFA / passkey RPCs. Those stay in Zitadel Console permanently.

### Migration from Phase 9b

The following RPCs originally listed under `PortalService` in [Phase 9b](phase-09b-portal-spa.md) move to `AdminService` here:

```
CreateUpstream, UpdateUpstream, DeleteUpstream,
UpdateTenantSettings
```

`PortalService` keeps the user-scoped subset: `ListUpstreams`, `StartConnect`, `SubmitUpstreamAPIKey`, `SetUpstreamLinkEnabled`, `Disconnect`, `ListMCPClients`, `RevokeMCPClient`. The session bootstrap RPC (`GetSession`) moves to the shared `limen.session.v1.SessionService` — see [Phase 9d](phase-09d-shared-session-service.md).

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
- **IdP-only delegation guard**: a `grep` in CI fails the build if a Limen file references `AddOrgOIDCIDP`, `AddOrgSAMLIDP`, `UpdateOrgIDP`, `RemoveOrgIDP`, or `tenant_idp_configurations` — those names belong to Zitadel only. The guard does **not** block member-management identifiers (`InviteMember` / `RemoveMember` / `UpdateMemberRole`) — those are valid Limen surface area from v2 onward.

## Risks

- **Captcha vendor lock-in**: hCaptcha vs reCAPTCHA vs Cloudflare Turnstile is a config knob; the SPA reads the chosen provider's site key from `window.__LIMEN_CONFIG__` injected by the static host. Document the matrix in [Phase 11](phase-11-production-deployment.md).
- **Signup → email delivery**: hostile networks may block Zitadel's outbound SMTP. Dev uses MailHog; prod must verify SMTP relay health via the resilience breaker (Phase 10).
- **Two SPAs, one cookie**: a future redesign that needs different idle-timeout policies for admin vs portal would have to split the cookie. Out of scope; flagged here so we don't accidentally hard-code "admin has the same timeout as portal" as an invariant.
- **Zitadel Console URL shape changes** between major versions could break the deep-links in `ZitadelDirectory.vue`. Mitigation: keep the path templates in one Vue constants file, smoke-test them in CI against the dev Zitadel container, and pin a known-good Zitadel image tag in [Phase 0](phase-00-dev-environment.md).

## Checklist

### v1 (this phase)

- [x] `proto/limen/admin/v1/admin.proto` defines `AdminService` with upstream catalog CRUD + tenant-settings RPCs — no member RPCs at v1; **signup is a sibling `proto/limen/signup/v1/signup.proto`/`SignupService`** instead of a skip-list, so the three layered interceptors stay uniform
- [x] `buf generate` produces Go bindings under `internal/admin/adminv1/`, `internal/signup/signupv1/`, and TS under `web/admin/src/gen/limen/{admin,signup}/v1/` (template `buf.gen.admin-ts.yaml`)
- [ ] `internal/admin/` package implements the above RPCs — slice 1 ships the handler skeleton (every RPC returns `CodeUnimplemented`) plus the three layered interceptors and a default-deny `requiredRole` map (`DeleteTenant`=`owner`, everything else=`admin`); subsequent slices implement bodies
- [x] `CreateUpstream` runs `IndexUpstream` inline for tenant-mode strategies (`none`, `static_header` tenant-mode) and returns `{requires_admin_link: false, tools: [...]}`; for per-user strategies it returns `{requires_admin_link: true, connect_url}` and leaves the catalog empty until an admin/owner completes the connect flow
- [x] `ReindexUpstreamCatalog` rejects per-user-strategy calls from admins who hold no enabled link to the upstream with `failed_precondition`
- [x] `Upstreams.vue` blocks the "upstream ready" state on `tool_count > 0` and renders the admin-link modal as the mandatory next step after creating an OAuth/per-user upstream
- [x] `UpstreamDetail.vue` includes the `ContextJsonEditor.vue` for `defaults_json`: textarea-backed JSON.parse linter with live size + parse validation, schema hints per strategy, and a disabled merged-preview placeholder (will use `AdminService.PreviewUpstreamContext` once a user-picker lands)
- [x] `AdminService.UpdateUpstream` calls `contextblob.ValidateContextBlob` on `defaults_json` (inside `upstream.Service`) and maps failures to Connect `invalid_argument` with a structured `field_path` detail
- [x] `web/shared/src/components/ContextJsonEditor.vue` and `web/shared/src/lib/upstreamContextHints.ts` are reusable from the customer portal (Phase 9b) for the per-link `context_json` editor
- [ ] `Dashboard.vue` renders the onboarding bento (welcome + setup-progress + 3 task cards + system-health empty state + quick-resources) and computes step completion from `ListUpstreams` + `tenant_settings.invited_team_at` + `tenant_settings.configured_at`
- [ ] `ContextualSearch.vue` honours `route.meta.search` for `filter` vs `palette` vs hidden modes; placeholder text is route-driven
- [ ] `Members.vue` (v1) wraps `ZitadelDirectory.vue` and renders deep-links for invite / role / remove / IdP / branding / login policy / personal profile, populated with the calling tenant's `orgId`
- [ ] `Settings.vue` covers tenant name + read-only `PublicID` + read-only Zitadel identity panel + DCR redirect-URI allowlist editor + Danger Zone (`DeleteTenant`)
- [ ] `internal/admin/` contains **no** `members.go` and **no** `idp.go` at v1; reviewers may approve `members.go` at v1.5+ but never `idp.go`
- [ ] No `tenant_idp_configurations` migration ever; no `internal/zitadel/` wrappers for `AddOrgOIDCIDP`, `AddOrgSAMLIDP`, etc.
- [ ] `StartSignup` is captcha-gated and per-IP rate-limited; returns generic errors
- [ ] `CompleteSignup` is keyed off the `pending_signup` cookie and is idempotent
- [ ] Signup completes a full round-trip: name + email → MailHog → password set → admin SPA
- [ ] Admin SPA routes lazy-loaded; all v1 pages above implemented
- [x] `GET /auth/discovery` returns the configured Zitadel issuer URL for the SPA to build Console deep-links
- [ ] Customer portal SPA shows an "Admin" chip iff the session carries `owner` or `admin`
- [ ] Single portal cookie at `Path=/t/<tenant>` covers both `/portal/` and `/admin/`; role interceptor is the only authorization boundary
- [ ] Vite dev proxy + Phase 11 Caddyfile route `/t/*/admin/api/*`, `/signup`, `/auth/signup`, `/auth/discovery` to Limen
- [ ] Bundle-separation test: a clean-cache `member` browsing `/portal/` does not fetch the admin bundle
- [ ] Phase 9b proto and handlers trimmed: admin RPCs moved out; Phase 9b doc updated to point at this phase
- [ ] Phase 4 _Self-service delegation_ table updated to reflect v1 / v1.5 / v2 member-management phasing
- [ ] `AGENTS.md` build section updated

### v1.5 (follow-up — read-only members table)

- [ ] `proto/limen/admin/v1/admin.proto` gains `ListMembers` (read-only)
- [ ] `internal/zitadel/management.go` added — thin Management-API client returning Limen-shaped DTOs
- [ ] `internal/admin/members.go` implements `ListMembers` via the Management client
- [ ] `Members.vue` swaps from `ZitadelDirectory.vue` to the inline table layout (avatar / name / email / role / status / last login / actions); action items still deep-link to Console
- [ ] `Dashboard.vue` task card "Invite Your Team" scrolls to the inline table when clicked (no behavioural change to the completion rule)

### v2 (follow-up — write ownership of members)

- [ ] `proto/limen/admin/v1/admin.proto` gains `InviteMember`, `UpdateMemberRole`, `RemoveMember`, `ResendInvite`
- [ ] `internal/admin/members.go` expands to handle writes via the Management client
- [ ] `Members.vue` actions wire to the new RPCs (Invite User CTA, role-change dropdown, remove action)
- [ ] IdP / branding / login-policy / profile cards remain deep-linked permanently
- [ ] `TransferOwnership` stays deep-linked (deliberately manual)
