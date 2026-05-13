# Phase 9 — Portal backend (Connect-RPC) + Vue 3 SPA

**Depends on**: Phases 4 (portal session), 7 (upstream connect/disconnect)
**Unblocks**: nothing (final user-facing feature before hardening)

## Goal

Ship the operator/user-facing web portal: a Vue 3 + Vite SPA served from the binary, backed by a strongly-typed [Connect-RPC](https://connectrpc.com/) API mounted at `/t/{tenant}/api/portal.v1.PortalService/*`. The portal lets users log in (via Zitadel OIDC — see [Phase 4](phase-04-tenant-auth-session.md)), link/unlink upstreams, see their MCP clients (DCR'd through Limen's proxy into Zitadel), and lets admins/owners manage members, invitations, and upstream configurations.

Password management, MFA, and email verification all live in Zitadel's hosted UI — the portal links out to it rather than reimplementing it.

The SPA is **not** embedded in the Go binary. It is built to a static `web/dist/` directory and served by whatever static host the deployment picked — Caddy `file_server` for self-hosted Compose, or Cloudflare Pages for managed deployments. Limen ships only the JSON/Connect-RPC API plus the OIDC, OAuth-proxy, MCP, and upstream-connect routes; it has no HTML responsibility.

The SPA and the Limen API are served from the **same origin** in v1 (e.g. both under `https://limen.example.com`). That preserves the `Path=/t/<slug>; SameSite=Lax` portal-cookie isolation set up in [Phase 4](phase-04-tenant-auth-session.md) without CORS preflight or `SameSite=None` complications. Splitting the SPA onto a different origin is possible but explicitly out of scope for v1 — it requires CORS-with-credentials plus `SameSite=None; Secure` cookies and is called out under [Risks](#risks).

## Design

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
// /t/{slug}/portal/api/. Tenant administrators self-serve member
// management, role grants, password / MFA / passkey enrollment, and
// external IdP federation directly from the [Zitadel Console](https://zitadel.com/docs/concepts/features/selfservice)
// — see Phase 4's "Self-service delegation" table. The Limen admin
// surface ([Phase 9b](phase-09b-tenant-admin-spa.md)) covers only
// Limen-domain operations: upstream catalog CRUD, tenant settings,
// and self-serve tenant signup.
service PortalService {
  // Public — no session required. The SPA calls this on boot to discover whether
  // the user already has a valid portal session; if not, the SPA redirects the
  // browser to /auth/login?tenant=<slug>&return_to=... (which initiates the
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
2. For each upstream advertised by the admin, the SPA shows the connection state for *this user*.
3. Clicking **Connect**:
   - `mcp_spec` → SPA calls `StartConnect`, which returns the Zitadel-side authorize URL; the browser is redirected, the user consents, Limen completes the OAuth dance, persists the `UpstreamLink`, and redirects back to the portal.
   - `static_header` user-mode → `StartConnect` returns a relative SPA path; the SPA opens a modal that takes the API key, then submits it via `SubmitUpstreamAPIKey`. Limen encrypts it with AAD `tenant|user|"upstream.extra"` and persists the `UpstreamLink`.
   - `none` / `static_header` tenant-mode → no action needed; the tools are already visible.
4. Once linked, the user sees a green badge and the upstream's tools become visible to the MCP RS for that user (Phase 8).
6. If Limen's auto-disable logic trips for that user (sustained refresh or tool-call failures — see Phase 7), the row flips to an `auto_disabled` state with a banner explaining the reason and last failure timestamp. The user clicks **Re-enable** to clear `AutoDisabledAt` and let the next request try again, or **Reconnect** when the row is also `needs_relink`.
7. The user can return to the Upstreams page at any time to:
   - **Disable** a link (`SetUpstreamLinkEnabled(false)`) — credentials are kept, tools immediately disappear from MCP `tools/list`.
   - **Enable** a previously disabled link — tools reappear without re-doing auth.
   - **Re-enable** an auto-disabled link via `SetUpstreamLinkEnabled(true)` (the RPC clears `AutoDisabledAt` + `ConsecutiveFailures` server-side when the caller is the owner of the link).
   - **Rotate** a `static_header` user-mode key by re-submitting through `SubmitUpstreamAPIKey`.
   - **Disconnect** — deletes the `UpstreamLink`; OAuth tokens are revoked at the upstream when possible.

### Backend (`internal/portal/`)

```
internal/portal/
├── service.go         // implements PortalServiceHandler
├── interceptor.go     // resolves *Tenant + *User from URL slug + portal cookie
├── upstreams.go       // ListUpstreams, StartConnect, SubmitUpstreamAPIKey, SetUpstreamLinkEnabled, Disconnect
├── mcpclients.go      // ListMCPClients, RevokeMCPClient
└── errors.go          // Connect error mapping
```

Admin / owner operations split two ways: anything Zitadel ships as self-service (members, invites, role grants, password / MFA, external IdP federation, branding) goes to Zitadel Console — see Phase 4's _Self-service delegation_ table. Limen-domain admin operations (upstream catalog CRUD, tenant settings, self-serve tenant signup) live in `internal/admin/` under [Phase 9b](phase-09b-tenant-admin-spa.md).

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
| `tenancyInterceptor`       | Extract `{tenant}` slug from URL (via chi context), resolve, place in ctx.                                                                                                                                           |
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

All admin / owner RPCs live in [Phase 9b](phase-09b-tenant-admin-spa.md)'s `AdminService` and have their own required-role table there.

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

Stack:

- **Vue 3** with Composition API + `<script setup>`.
- **Vite** for dev/build.
- **Pinia** for state (`session`, `upstreams`).
- **Vue Router** for `/login`, `/`, `/upstreams`, `/mcp-clients`, `/settings`. Base path is `/t/<slug>/portal`; resolved at boot from `window.location.pathname`. (Member management lives in the admin SPA at `/t/<slug>/admin/` — see [Phase 9b](phase-09b-tenant-admin-spa.md) — which links out to Zitadel Console.)
- **`@connectrpc/connect-web`** for typed RPC calls. Codegen output lives under `web/src/gen/`.
- **Tailwind CSS** (or plain CSS — operator preference; keep dependencies minimal).
- **No SSR**.

### Login flow

Login is delegated entirely to Zitadel via OIDC (Phase 4). The SPA's role is minimal:

1. SPA boots, calls `GetSession`.
2. If unauthenticated, the `/login` route renders a single "Sign in" button.
3. Clicking it navigates the browser to `/auth/login?tenant=<slug>&return_to=<current path>`.
4. Limen's `/auth/login` handler signs state and redirects to Zitadel's authorize endpoint.
5. Zitadel renders its hosted login UI (with MFA, password reset, etc.).
6. Zitadel returns to `/auth/callback`, Limen sets the portal cookie, redirects to `/t/<slug>/portal/<return_to>`.
7. SPA reloads, `GetSession` succeeds, dashboard renders.

No credentials ever traverse Limen. The SPA never sees a password input.

### Build & deploy

- `pnpm install && pnpm build` in `web/` outputs to `web/dist/`. That directory is the entire deliverable — a tree of hashed JS/CSS/asset files plus `index.html`.
- **Self-hosted (Caddy `file_server`)**: the production reverse proxy mounts `web/dist/` and serves any path not matched by the Limen route rules from it, with `try_files {path} /index.html` so the SPA's client-side router handles deep links. Configured in [Phase 11](phase-11-production-deployment.md).
- **Managed (Cloudflare Pages)**: push the same `web/dist/` to a Pages project (`wrangler pages deploy`); Caddy reverse-proxies non-API paths to the Pages origin via a `reverse_proxy` block scoped to the SPA URL prefixes. CI builds and deploys; the Go side is unchanged.
- **No `//go:embed`, no `internal/portal/spa.go`, no SPA fallback handler.** The Go HTTP router only knows about `/t/{slug}/api/*`, `/auth/*`, `/oauth/*`, `/mcp/*`, and `/t/{slug}/upstream/*`.
- **Base path**: Vite is built with `base: "./"` so the bundle works regardless of where it's mounted. At runtime, the SPA reads `window.location.pathname` to discover the `/t/<slug>/portal/` prefix and feeds it to Vue Router as `createWebHistory(<basePath>)`. Same trick lets a single build serve every tenant.
- **CSP**: set by the static host, not Limen. Recommended policy: `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' https://auth.limen.example.com; img-src 'self' data:; frame-ancestors 'none'` — documented in the Caddyfile / Pages headers file. The Zitadel origin is added to `connect-src` for the redirect handshake.
- **Caching**: `Cache-Control: no-store` on `index.html`; long-cache (`public, max-age=31536000, immutable`) on the hashed assets — standard Vite output.

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
- `pnpm build` clean; the resulting `web/dist/` is what the static host serves.
- Connect handlers respond correctly to:
  - Missing session → `unauthenticated`.
  - Wrong role → `permission_denied`.
  - Cross-tenant request (slug A cookie used on slug B URL) → `unauthenticated`.
  - Successful happy paths for each RPC.
- Dev workflow: `pnpm dev` runs Vite locally; `vite.config.ts` proxies `/t/*/api/*`, `/auth/*`, `/oauth/*`, `/mcp/*`, `/upstream/*` to the local Limen process so the SPA + API stay same-origin in dev too.
- Cypress / Playwright smoke (optional in v1) covering: login → connect Atlassian upstream (mocked) → tool visible → disconnect.

## Risks

- **Static-host divergence**: Caddy `file_server` and Cloudflare Pages have slightly different semantics for SPA fallback and headers. Both are documented in Phase 11; the dev / CI pipeline targets Caddy by default so behavior is predictable, and a deployment-mode flag in the runbook covers Pages.
- **Cross-origin SPA**: a future managed deployment that puts the SPA on a different hostname than the API (e.g. `app.example.com` + `api.example.com`) breaks the cookie story. Out of scope for v1; if revisited, the migration is cookie `SameSite=None; Secure` + CORS-with-credentials, **or** swapping cookies for short-lived bearer tokens held in SPA memory.
- **Browser caching of `index.html`**: serve with `Cache-Control: no-store`; assets get long-cache + hashed filenames from Vite.
- **TypeScript code-gen drift**: lock `buf` version and the codegen plugin versions in `buf.gen.yaml`.

## Checklist

- [ ] `ListUpstreams` returns per-user link state (`none` / `connected` / `disabled` / `auto_disabled` / `needs_relink`) and strategy sub-mode, so the SPA can pick the right CTA, plus the last-failure reason + timestamp for auto-disabled rows
- [ ] `SetUpstreamLinkEnabled(true)` on an auto-disabled link clears `AutoDisabledAt` + `ConsecutiveFailures` server-side
- [ ] `SubmitUpstreamAPIKey` persists the user-supplied secret via the `static_header` strategy, AAD `tenant|user|"upstream.extra"`; never logs the key; supports rotation (overwrite)
- [ ] `SetUpstreamLinkEnabled` flips `UpstreamLink.Enabled` without touching stored credentials
- [ ] Upstreams page renders the right CTA per row (Connect / Enter API key / Enable / Disable / Disconnect) and lets the user rotate keys for `static_header` user-mode
- [ ] `proto/limen/portal/v1/portal.proto` with the full `PortalService` definition
- [ ] `buf.yaml` and `buf.gen.yaml` at the repo root
- [ ] Generated Go bindings under `internal/portal/portalv1/` and `…/portalv1connect/`
- [ ] Generated TS bindings under `web/src/gen/`
- [ ] `internal/portal/` package implements all RPCs against `storage.Session(ctx)`
- [ ] Tenancy interceptor populates ctx from URL slug
- [ ] Portal-session interceptor populates `*User` + roles (from the Zitadel project-roles claim) from cookie
- [ ] Role interceptor enforces the requiredRole table against ctx roles; unknown methods default-deny
- [ ] No Limen RPC mutates Zitadel user grants — `InviteMember`, `UpdateMemberRole`, `RemoveMember`, `TransferOwnership` are **not** in any Limen proto; the SPA renders a deep-link card pointing at Zitadel Console for these operations (see [Phase 9b](phase-09b-tenant-admin-spa.md))
- [ ] No `tenant_id` in request messages anywhere in the proto
- [ ] Vue 3 + Vite + Pinia + Vue Router + `@connectrpc/connect-web` SPA scaffolded under `web/`
- [ ] Pages: Login, Dashboard, Upstreams, Members, MCP Clients, Settings, Consent
- [ ] SPA base path resolved at boot from `/t/<slug>/portal/`
- [ ] Login flow uses classic POST + cookie (no JSON), CSRF via double-submit cookie
- [ ] SPA built to `web/dist/`; no `//go:embed`; no SPA fallback handler in Go
- [ ] Static-host wiring documented in Phase 11 for both Caddy `file_server` (self-hosted) and Cloudflare Pages (managed); both keep the SPA same-origin with the API
- [ ] CSP header set by the static host (Caddy directive or Pages `_headers`)
- [ ] `vite.config.ts` proxies API / auth / oauth / mcp / upstream paths to local Limen in dev so SPA + API stay same-origin during development
- [ ] `AGENTS.md` build section updated with `buf generate` and `pnpm build`
- [ ] Unit tests for the role-enforcement interceptor (matrix of method × role)
- [ ] Smoke test or manual run-through of the entire SPA flow
