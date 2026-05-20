# Phase 9b — Portal backend (Connect-RPC) + Vue 3 SPA

**Depends on**: [Phase 9a](phase-09a-binary-split.md) (binary layout — this phase's deliverables live in `cmd/portal/` + `web/`), Phase 4 (portal session), Phase 7 (upstream connect/disconnect)
**Unblocks**: nothing (final user-facing feature before hardening)

## Goal

Ship the operator/user-facing web portal: a Vue 3 + TypeScript + Vite SPA, backed by a strongly-typed [Connect-RPC](https://connectrpc.com/) API mounted at `/t/{tenant}/api/portal.v1.PortalService/*`. The portal lets users log in (via Zitadel OIDC — see [Phase 4](phase-04-tenant-auth-session.md)), link/unlink upstreams, see their MCP clients (DCR'd through Limen's proxy into Zitadel), and lets admins/owners manage members, invitations, and upstream configurations.

Identity, authentication, and authorization are **fully delegated to Zitadel**. The SPA never renders a password field. The portal backend never validates a password. Sessions are minted by Zitadel and brokered into a portal cookie by [Phase 4](phase-04-tenant-auth-session.md); roles come from the cached `urn:zitadel:iam:org:project:roles` claim; password / MFA / passkey enrollment / external IdP federation / member management / invitations all live in Zitadel Console, deep-linked from the SPA. Limen never duplicates an identity primitive Zitadel already ships.

The SPA is **not** embedded in the Go binary. It is built to a static `web/dist/` directory and deployed to **Cloudflare Pages** (managed deployments) or served by Caddy `file_server` (self-hosted Compose). Limen ships only the JSON/Connect-RPC API plus the OIDC, OAuth-proxy, MCP, and upstream-connect routes; it has no HTML responsibility.

The SPA and the Limen API are served from the **same origin** in v1 (e.g. both under `https://limen.example.com`). That preserves the `Path=/t/<tenant>; SameSite=Lax` portal-cookie isolation set up in [Phase 4](phase-04-tenant-auth-session.md) without CORS preflight or `SameSite=None` complications. For Cloudflare Pages deployments, this is achieved by fronting Pages with the same Limen-owned hostname via a Cloudflare Worker or Pages Functions route that proxies `/t/*/api/*`, `/auth/*`, `/oauth/*`, `/mcp/*`, and `/t/*/upstream/*` to the Limen origin — the SPA itself is served from `/` on the same host. Splitting the SPA onto a different origin is possible but explicitly out of scope for v1 — it requires CORS-with-credentials plus `SameSite=None; Secure` cookies and is called out under [Risks](#risks).

## Design

> **Process / binary boundary**: the portal Connect-RPC API runs in its own binary
> (`cmd/portal/`) — see [Phase 9a](phase-09a-binary-split.md). This phase
> describes the portal API + SPA themselves; deployment topology and the
> per-binary build / image rules live in 9a.

### Why Connect-RPC

- Browser-native: no gRPC streaming complications, no WASM payload.
- One source of truth for types: `proto/limen/portal/v1/portal.proto`.
- Codegen for Go (server) and TypeScript (client) via `buf`.
- Built-in interceptors for tenant resolution and session validation.

### `proto/limen/portal/v1/portal.proto`

Single service, methods grouped by access level.

```proto
syntax = "proto3";
package limen.portal.v1;

// PortalService is the **user-scoped** customer surface mounted at
// /t/{tenant}/portal/api/. Tenant administrators self-serve member
// management, role grants, password / MFA / passkey enrollment, and
// external IdP federation directly from the [Zitadel Console](https://zitadel.com/docs/concepts/features/selfservice)
// — see Phase 4's "Self-service delegation" table. The Limen admin
// surface ([Phase 9c](phase-09c-tenant-admin-spa.md)) covers only
// Limen-domain operations: upstream catalog CRUD, tenant settings,
// and self-serve tenant signup.
service PortalService {
  // Public — no session required. The SPA calls this on boot to discover whether
  // the user already has a valid portal session; if not, the SPA redirects the
  // browser to /auth/login?tenant=<tenant>&return_to=... (which initiates the
  // Zitadel OIDC flow — see Phase 4).
  rpc GetSession(GetSessionRequest) returns (GetSessionResponse);

  // Authenticated (any role)
  rpc ListUpstreams(ListUpstreamsRequest) returns (ListUpstreamsResponse);
  rpc StartConnect(StartConnectRequest) returns (StartConnectResponse);
  rpc SubmitUpstreamAPIKey(SubmitUpstreamAPIKeyRequest) returns (SubmitUpstreamAPIKeyResponse);  // static_header user-mode: paste/rotate the per-user secret
  rpc SetUpstreamLinkEnabled(SetUpstreamLinkEnabledRequest) returns (SetUpstreamLinkEnabledResponse);  // toggle without dropping credentials
  rpc Disconnect(DisconnectRequest) returns (DisconnectResponse);  // removes the UpstreamLink entirely
  rpc ListMCPClients(ListMCPClientsRequest) returns (ListMCPClientsResponse);
  rpc RevokeMCPClient(RevokeMCPClientRequest) returns (RevokeMCPClientResponse);
  // Password / MFA management is delegated to Zitadel — the SPA links out to
  // the Zitadel self-service console.
}
```

Request messages **do not carry `tenant_id`** — the interceptor reads it from the URL path. Authoritative tenant binding is server-side.

`ListUpstreams` returns, for each upstream visible to the tenant: the public ID, name, strategy type + sub-mode (e.g. `static_header.user`), MCP server URL, whether the strategy requires a per-user link, and — for the calling user — the link state: `none` / `connected` / `disabled` / `auto_disabled` / `needs_relink`. The SPA uses this to render the right CTA ("Connect", "Enter API key", "Disable", "Enable", "Re-enable", "Reconnect", "Disconnect") on each row, plus a short status line (e.g. "auto-disabled after 8 consecutive failures, last error 12 min ago").

Workflow recap (covers both OAuth-protected and header-authenticated upstreams):

1. The user logs into the portal via Zitadel (Phase 4) and lands on the Upstreams page.
2. For each upstream advertised by the admin, the SPA shows the connection state for _this user_.
3. Clicking **Connect**:
   - `mcp_spec` → SPA calls `StartConnect`, which returns the Zitadel-side authorize URL; the browser is redirected, the user consents, Limen completes the OAuth dance, persists the `UpstreamLink`, and redirects back to the portal.
   - `static_header` user-mode → `StartConnect` returns a relative SPA path; the SPA opens a modal that takes the API key, then submits it via `SubmitUpstreamAPIKey`. Limen encrypts it with AAD `tenant|user|"upstream.extra"` and persists the `UpstreamLink`.
   - `none` / `static_header` tenant-mode → no action needed; the tools are already visible.
4. Once linked, the user sees a green badge and the upstream's tools become visible to the MCP RS for that user (Phase 8).
5. If Limen's auto-disable logic trips for that user (sustained refresh or tool-call failures — see Phase 7), the row flips to an `auto_disabled` state with a banner explaining the reason and last failure timestamp. The user clicks **Re-enable** to clear `AutoDisabledAt` and let the next request try again, or **Reconnect** when the row is also `needs_relink`.
6. The user can return to the Upstreams page at any time to:
   - **Disable** a link (`SetUpstreamLinkEnabled(false)`) — credentials are kept, tools immediately disappear from MCP `tools/list`.
   - **Enable** a previously disabled link — tools reappear without re-doing auth.
   - **Re-enable** an auto-disabled link via `SetUpstreamLinkEnabled(true)` (the RPC clears `AutoDisabledAt` + `ConsecutiveFailures` server-side when the caller is the owner of the link).
   - **Rotate** a `static_header` user-mode key by re-submitting through `SubmitUpstreamAPIKey`.
   - **Disconnect** — deletes the `UpstreamLink`; OAuth tokens are revoked at the upstream when possible.

### Backend (`internal/portal/`)

```
internal/portal/
├── service.go         // implements PortalServiceHandler
├── interceptor.go     // resolves *Tenant + *User from URL tenant id + portal cookie
├── upstreams.go       // ListUpstreams, StartConnect, SubmitUpstreamAPIKey, SetUpstreamLinkEnabled, Disconnect
├── mcpclients.go      // ListMCPClients, RevokeMCPClient
└── errors.go          // Connect error mapping
```

**Boundary with Phase 7.** [Phase 7](phase-07-outbound-upstream.md) ships the upstream linking engine and exposes a plain Go API (`internal/upstream.Service`) with `StartConnect(ctx, upstreamName, returnTo) (redirectURL string, err error)`, `Disconnect(ctx, upstreamName) error`, and `PersistUserStaticHeaderSecret(ctx, upstreamName, secret) error`. Phase 9b's `StartConnect`, `Disconnect`, and `SubmitUpstreamAPIKey` Connect-RPC handlers are thin wrappers around those methods; they perform no OAuth/strategy logic of their own. The only HTTP route Phase 7 owns is `GET /t/{tenant}/upstream/{name}/callback` — the protocol-mandated OAuth redirect URI, behind `tenancy.RequireTenant` + `OIDC.RequireSession`. Everything else is Connect-RPC.

Admin / owner operations split two ways: anything Zitadel ships as self-service (members, invites, role grants, password / MFA, external IdP federation, branding) goes to Zitadel Console — see Phase 4's _Self-service delegation_ table. Limen-domain admin operations (upstream catalog CRUD, tenant settings, self-serve tenant signup) live in `internal/admin/` under [Phase 9c](phase-09c-tenant-admin-spa.md).

Mounting:

```go
mux.Mount("/t/{tenant}/api", portalv1connect.NewPortalServiceHandler(
    svc,
    connect.WithInterceptors(
        tenancyInterceptor,
        portalSessionInterceptor,
        roleInterceptor,
    ),
))
```

Each interceptor:

| Interceptor                | Responsibility                                                                                                                                                                                                       |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tenancyInterceptor`       | Extract `{tenant}` segment from URL (via chi context), resolve, place in ctx.                                                                                                                                        |
| `portalSessionInterceptor` | Decrypt cookie, validate via Zitadel `SessionService.GetSession` (60 s positive cache), place `*User` + roles (from the cached `urn:zitadel:iam:org:project:roles` claim) in ctx. 401 (`unauthenticated`) otherwise. |
| `roleInterceptor`          | Map RPC method → required role (annotation table); enforce against the roles in ctx. 403 (`permission_denied`) otherwise.                                                                                            |

Required-role table (illustrative):

```go
var requiredRole = map[string]Role{
    "GetSession":          RoleAny,
    "ListUpstreams":       RoleMember,
    "StartConnect":        RoleMember,
    "SubmitUpstreamAPIKey":RoleMember,
    "SetUpstreamLinkEnabled":RoleMember,
    "Disconnect":          RoleMember,
    "ListMCPClients":      RoleMember,
    "RevokeMCPClient":     RoleMember,
}
```

All admin / owner RPCs live in [Phase 9c](phase-09c-tenant-admin-spa.md)'s `AdminService` and have their own required-role table there.

### Frontend (`web/`)

```
web/
├── package.json
├── vite.config.ts
├── tsconfig.json
├── buf.gen.yaml       // generates TS clients from proto/
├── public/
└── src/
    ├── main.ts
    ├── App.vue
    ├── router/index.ts
    ├── stores/
    │   ├── session.ts
    │   └── upstreams.ts
    ├── api/client.ts  // wraps @connectrpc/connect-web
    ├── pages/
    │   ├── Login.vue          // landing page; auto-redirects to /auth/login
    │   ├── Dashboard.vue
    │   ├── Upstreams.vue
    │   ├── MCPClients.vue
    │   └── Settings.vue
    └── components/
        ├── UpstreamCard.vue
        └── …
```

Stack — pinned at "latest stable" for v1; the lockfile is the source of truth, this list is the contract:

- **Node.js LTS** (currently v22.x as of May 2026). CI matrix runs on the active LTS only; we do not chase Current.
- **pnpm v11** as the only supported package manager. `package.json` declares `"packageManager": "pnpm@11.x.x"` and CI verifies via Corepack; `npm install` / `yarn install` are not supported.
- **Vue 3** (latest 3.x), Composition API + `<script setup>` + `<script setup lang="ts">` exclusively.
- **TypeScript** (latest 5.x), `strict: true`, no implicit `any`, no untyped fetch / RPC boundaries.
- **Vite** (latest 5.x) for dev/build. `vue-tsc` runs as part of `pnpm build` so type errors fail the build.
- **Pinia** (latest 2.x) for state (`session`, `upstreams`).
- **Vue Router** (latest 4.x) for `/login`, `/`, `/upstreams`, `/mcp-clients`, `/settings`. Base path is `/t/<tenant>/portal`; resolved at boot from `window.location.pathname`. (Member management lives in the admin SPA at `/t/<tenant>/admin/` — see [Phase 9c](phase-09c-tenant-admin-spa.md) — which links out to Zitadel Console.)
- **`@connectrpc/connect-web`** (latest) for typed RPC calls. Codegen output lives under `web/src/gen/`.
- **Tailwind CSS** (latest 4.x) for styling — keeps dependency count low and gives us consistent design tokens.
- **ESLint** + **Prettier** + **`@vue/eslint-config-typescript`** at latest stable; one config, no per-package overrides.
- **Vitest** (latest) for unit tests; **Playwright** (latest) for the smoke test path.
- **No SSR.** **No Nuxt.** **No state-management lib other than Pinia.** **No alternative router.**

Dependency policy: every direct dependency must be "latest stable" at the time of the lockfile bump. Renovate (or `pnpm up --latest` run quarterly + on security advisories) keeps it that way. Pre-1.0 dependencies are avoided where a 1.0+ alternative exists.

### Login flow — Zitadel-only

Login and every authorization decision are delegated to Zitadel. The
SPA owns navigation and presentation; it does not own identity.

1. SPA boots, calls `GetSession` (the only RPC that does not require an authenticated session).
2. If unauthenticated, the `/login` route renders a single "Sign in with Zitadel" button.
3. Clicking it navigates the browser to `/auth/login?tenant=<tenant>&return_to=<current path>`.
4. Limen's `/auth/login` handler ([Phase 4](phase-04-tenant-auth-session.md)) signs state and redirects to Zitadel's authorize endpoint.
5. Zitadel renders its hosted login UI — password, MFA, passkeys, external IdP federation, password reset, account recovery, email / phone verification. All of it. Limen has zero UI responsibility here.
6. Zitadel returns to `/auth/callback`. Limen exchanges the code, validates the ID token (issuer + audience + signature via JWKS), reads the `urn:zitadel:iam:org:project:roles` claim, sets the portal cookie (`Path=/t/<tenant>; HttpOnly; Secure; SameSite=Lax`), redirects to `/t/<tenant>/portal/<return_to>`.
7. SPA reloads, `GetSession` succeeds, dashboard renders.

Guarantees this flow buys us:

- **No credentials ever traverse Limen.** Not on login, not on rotation, not on recovery.
- **No Limen-side identity tables.** No `users.password_hash`, no `users.totp_secret`, no `password_reset_tokens`. The `User` row only mirrors `ZitadelSubject` + display fields.
- **Authorization is claim-driven.** The portal-session interceptor populates `*User` + roles from the cached Zitadel claim; the `roleInterceptor` enforces against the per-RPC `requiredRole` table. Backend RPCs never re-derive role membership from a Limen-side table.
- **Self-service primitives are deep-linked, not re-implemented.** Profile, password change, MFA, passkey enrollment, social logins, session listing, and member / role grant management all open Zitadel Console in a new tab. The SPA renders the deep-link card; it does not render the form.
- **Session revocation is Zitadel-side.** Logout calls Zitadel's end-session endpoint (with `post_logout_redirect_uri` back to the SPA) and clears the portal cookie. Forced revocation from a Zitadel admin (terminate session) causes the next `GetSession` to fail validation and the SPA falls back to the login route.

### Build & deploy

- **Toolchain**: Node LTS + pnpm v11, both pinned via Corepack (`"packageManager": "pnpm@11.x.x"` in `package.json`). CI uses `corepack enable && corepack prepare pnpm@<version> --activate`. No global pnpm install required on dev machines.
- `pnpm install --frozen-lockfile && pnpm build` in `web/` outputs to `web/dist/`. That directory is the entire deliverable — a tree of hashed JS/CSS/asset files plus `index.html`.
- **Managed deployment (Cloudflare Pages, default)**: GitHub Actions pushes `web/dist/` to a Pages project via `wrangler pages deploy`. Pages serves the SPA from the canonical host (e.g. `limen.example.com`); a Cloudflare Worker / Pages Functions route in front of Pages reverse-proxies the Limen API path prefixes (`/t/*/api/*`, `/auth/*`, `/oauth/*`, `/mcp/*`, `/t/*/upstream/*`) to the Limen origin so SPA and API stay same-origin from the browser's perspective. Pages handles TLS, HTTP/3, caching, and asset compression; the Go side is unchanged.
- **Self-hosted (Caddy `file_server`)**: the production reverse proxy mounts `web/dist/` and serves any path not matched by the Limen route rules from it, with `try_files {path} /index.html` so the SPA's client-side router handles deep links. Configured in [Phase 11](phase-11-production-deployment.md). Self-hosters who don't want a CDN run this profile and skip Cloudflare entirely.
- **No `//go:embed`, no `internal/portal/spa.go`, no SPA fallback handler.** The Go HTTP router only knows about `/t/{tenant}/api/*`, `/auth/*`, `/oauth/*`, `/mcp/*`, and `/t/{tenant}/upstream/*`.
- **Base path**: Vite is built with `base: "./"` so the bundle works regardless of where it's mounted. At runtime, the SPA reads `window.location.pathname` to discover the `/t/<tenant>/portal/` prefix and feeds it to Vue Router as `createWebHistory(<basePath>)`. Same trick lets a single build serve every tenant.
- **CSP**: set by the static host (Cloudflare Pages `_headers` file or Caddy directive), not Limen. Recommended policy: `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' https://auth.limen.example.com; img-src 'self' data:; frame-ancestors 'none'`. The Zitadel origin is added to `connect-src` for the redirect handshake (the Zitadel session-check XHR also rides this).
- **Caching**: `Cache-Control: no-store` on `index.html`; long-cache (`public, max-age=31536000, immutable`) on the hashed assets — standard Vite output. Cloudflare Pages applies these via the bundled `_headers` file.

### Build orchestration

Buf project at the repo root (`buf.yaml`, `buf.gen.yaml`):

- Go output → `internal/portal/portalv1` and `internal/portal/portalv1/portalv1connect`.
- TS output → `web/src/gen/`.

Make targets (or `go generate`):

```
buf generate   # regenerates Go + TS code from proto
go build ./...
(cd web && pnpm build)
```

`AGENTS.md` (Phase 10) documents this sequence.

### CSRF & content-type

Connect-RPC uses `Content-Type: application/connect+json` or `application/proto`, both browser-preflight-triggering. Browsers therefore won't send these CORS preflighted requests cross-origin from a third-party site, mitigating CSRF. The OIDC login flow itself is CSRF-protected by the signed `state` parameter (Phase 4).

## Deliverables

- New `proto/limen/portal/v1/portal.proto` plus `buf.yaml`, `buf.gen.yaml`.
- New `internal/portal/` package with interceptors and service handlers (no SPA / embed glue).
- New `web/` directory with the Vue 3 SPA, producing `web/dist/` as the deployable artifact.
- Updated `internal/transport/http.go` to mount only `/t/{tenant}/api/...` for the portal API; HTML serving lives in the reverse proxy / Pages.
- Caddyfile (for self-hosted) or Pages `_headers` + `_redirects` (for managed) entries documented in [Phase 11](phase-11-production-deployment.md).
- Updated `AGENTS.md` build section (Phase 10).

## Security & operational notes

- **No `tenant_id` in request payloads** — interceptor is the single source of truth. A test verifies this; add a lint rule (`grep` in CI) if practical.
- **`session` ctx propagates RLS** — every RPC handler calls `storage.Session(ctx)` and lets RLS enforce isolation. No raw DB use.
- **Owner-protected operations** that affect Zitadel user grants (invite, role change, member removal) are not implemented in Limen at all — they live in Zitadel Console and Zitadel enforces the org's own role-mutation rules. Limen-side ownership concerns (e.g. `DeleteTenant`) are owner-only via the project-roles claim.
- **MCP client revocation** calls Zitadel's Management API to delete the OIDC app, then removes the Limen `ZitadelApp` mirror row. Zitadel takes care of invalidating outstanding tokens issued for that client.
- **Consent** is shown by Zitadel's hosted UI (configurable per project) — no Limen-rendered consent screen.
- **CSP**: set by the static host (Caddy or Cloudflare Pages `_headers`), not by Limen. Recommended policy is documented above.
- **Same-origin assumption**: the SPA cookies (Phase 4) rely on this. Any deployment that splits the SPA onto a different origin must switch to `SameSite=None; Secure` cookies and a CORS-with-credentials policy on Limen — explicitly out of scope for v1.

## Verification

- `buf generate` produces compilable Go + TS.
- `go build ./...` clean.
- `pnpm install --frozen-lockfile && pnpm build` clean on Node LTS + pnpm v11; `vue-tsc` reports no type errors; the resulting `web/dist/` is what the static host serves.
- Connect handlers respond correctly to:
  - Missing session → `unauthenticated`.
  - Wrong role → `permission_denied`.
  - Cross-tenant request (tenant A cookie used on tenant B URL) → `unauthenticated`.
  - Successful happy paths for each RPC.
- Dev workflow: `pnpm dev` runs Vite locally; `vite.config.ts` proxies `/t/*/api/*`, `/auth/*`, `/oauth/*`, `/mcp/*`, `/upstream/*` to the local Limen process so the SPA + API stay same-origin in dev too.
- Cypress / Playwright smoke (optional in v1) covering: login → connect Atlassian upstream (mocked) → tool visible → disconnect.

## Risks

- **Static-host divergence**: Caddy `file_server` and Cloudflare Pages have slightly different semantics for SPA fallback and headers. Both are documented in Phase 11; the dev / CI pipeline targets Caddy by default so behavior is predictable, and a deployment-mode flag in the runbook covers Pages.
- **Cross-origin SPA**: a future managed deployment that puts the SPA on a different hostname than the API (e.g. `app.example.com` + `api.example.com`) breaks the cookie story. Out of scope for v1; if revisited, the migration is cookie `SameSite=None; Secure` + CORS-with-credentials, **or** swapping cookies for short-lived bearer tokens held in SPA memory.
- **Browser caching of `index.html`**: serve with `Cache-Control: no-store`; assets get long-cache + hashed filenames from Vite.
- **TypeScript code-gen drift**: lock `buf` version and the codegen plugin versions in `buf.gen.yaml`.

## Checklist

- [x] Toolchain pinned: Node LTS (active LTS only) + pnpm v11 via Corepack; `"packageManager": "pnpm@11.x.x"` in `web/package.json`; CI rejects builds run with npm or yarn
- [x] All direct dependencies at latest stable at lockfile bump time; Renovate (or quarterly `pnpm up --latest`) keeps it that way
- [x] `pnpm build` runs `vue-tsc --noEmit` and fails on type errors; TypeScript `strict: true`; no implicit `any`
- [x] `ListUpstreams` returns per-user link state (`none` / `connected` / `disabled` / `auto_disabled` / `needs_relink`) and strategy sub-mode, so the SPA can pick the right CTA, plus the last-failure reason + timestamp for auto-disabled rows
- [x] `SetUpstreamLinkEnabled(true)` on an auto-disabled link clears `AutoDisabledAt` + `ConsecutiveFailures` server-side
- [x] `SubmitUpstreamAPIKey` persists the user-supplied secret via the `static_header` strategy, AAD `tenant|user|"upstream.extra"`; never logs the key; supports rotation (overwrite)
- [x] `SetUpstreamLinkEnabled` flips `UpstreamLink.Enabled` without touching stored credentials
- [x] Upstreams page renders the right CTA per row (Connect / Enter API key / Enable / Disable / Disconnect) and lets the user rotate keys for `static_header` user-mode
- [x] `proto/limen/portal/v1/portal.proto` with the full `PortalService` definition
- [x] `buf.yaml` and `buf.gen.yaml` at the repo root
- [x] Generated Go bindings under `internal/portal/portalv1/` and `…/portalv1connect/`
- [x] Generated TS bindings under `web/src/gen/`
- [x] `internal/portal/` package implements all RPCs against `storage.Session(ctx)`
- [x] Tenancy interceptor populates ctx from URL tenant id
- [x] Portal-session interceptor populates `*User` + roles (from the Zitadel project-roles claim) from cookie
- [x] Role interceptor enforces the requiredRole table against ctx roles; unknown methods default-deny
- [x] No Limen RPC mutates Zitadel user grants — `InviteMember`, `UpdateMemberRole`, `RemoveMember`, `TransferOwnership` are **not** in any Limen proto; the SPA renders a deep-link card pointing at Zitadel Console for these operations (see [Phase 9c](phase-09c-tenant-admin-spa.md))
- [x] No `tenant_id` in request messages anywhere in the proto
- [x] Vue 3 (latest) + TypeScript (latest 5.x, strict) + Vite (latest 5.x) + Pinia + Vue Router 4 + `@connectrpc/connect-web` (latest) + Tailwind CSS 4 SPA scaffolded under `web/`
- [x] ESLint + Prettier + `@vue/eslint-config-typescript` at latest stable; single config; `pnpm lint` + `pnpm format:check` in CI
- [x] Vitest for unit tests; Playwright for the smoke path (login → connect upstream → tool visible → disconnect)
- [x] Login flow is Zitadel-only: SPA never renders a password field; portal backend never validates a password; profile / password / MFA / passkey / member-management / IdP federation surfaces are deep-linked to Zitadel Console
- [x] Authorization is claim-driven: `roleInterceptor` enforces `requiredRole` table against the cached `urn:zitadel:iam:org:project:roles` claim; no Limen-side role tables
- [x] Pages: Login, Dashboard, MCP Servers (Upstreams), MCP Clients, Settings — member management lives in the admin SPA's `ZitadelDirectory.vue` ([Phase 9c](phase-09c-tenant-admin-spa.md)), and consent is rendered by Zitadel's hosted UI, so neither is a portal page
- [x] SPA base path resolved at boot from `/t/<tenant>/portal/`
- [ ] Login flow uses classic POST + cookie (no JSON), CSRF via double-submit cookie
- [x] SPA built to `web/dist/`; no `//go:embed`; no SPA fallback handler in Go
- [ ] Cloudflare Pages deployment path: `wrangler pages deploy web/dist/` from CI; Worker / Pages Functions reverse-proxy for `/t/*/api/*`, `/auth/*`, `/oauth/*`, `/mcp/*`, `/t/*/mcp-servers/*/callback` so SPA + API stay same-origin
- [x] Static-host wiring documented in Phase 11 for both Cloudflare Pages (managed default) and Caddy `file_server` (self-hosted)
- [x] CSP header set by the static host (Caddy directive or Pages `_headers`)
- [x] `vite.config.ts` proxies API / auth / oauth / mcp / upstream paths to local Limen in dev so SPA + API stay same-origin during development
- [x] `AGENTS.md` build section updated with `buf generate` and `pnpm build`
- [x] Unit tests for the role-enforcement interceptor (matrix of method × role)
- [x] Smoke test or manual run-through of the entire SPA flow
- [ ] Integration test for `static_header` user-mode: submit key via `SubmitUpstreamAPIKey` → tool becomes visible in `ListTools` → `SetUpstreamLinkEnabled(false)` hides it → `SetUpstreamLinkEnabled(true)` makes it visible again, without re-submitting the key _(moved from [Phase 7](phase-07-outbound-upstream.md) — depends on this phase's portal Connect-RPC surface)_
- [ ] Integration test (recovery half): an auto-disabled link (set up by the [Phase 8](phase-08-per-tenant-injection.md) sustained-5xx test) recovers via `SetUpstreamLinkEnabled(true)` clearing `AutoDisabledAt` + `ConsecutiveFailures`; the next successful upstream call keeps it healthy _(moved from [Phase 7](phase-07-outbound-upstream.md) — the Re-enable CTA is a portal RPC owned by this phase)_
