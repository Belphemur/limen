# Phase 9 — Portal backend (Connect-RPC) + Vue 3 SPA

**Depends on**: Phases 4 (portal session), 7 (upstream connect/disconnect)
**Unblocks**: nothing (final user-facing feature before hardening)

## Goal

Ship the operator/user-facing web portal: a Vue 3 + Vite SPA served from the binary, backed by a strongly-typed [Connect-RPC](https://connectrpc.com/) API mounted at `/t/{tenant}/api/portal.v1.PortalService/*`. The portal lets users log in (via Zitadel OIDC — see [Phase 4](phase-04-tenant-auth-session.md)), link/unlink upstreams, see their MCP clients (DCR'd through Limen's proxy into Zitadel), and lets admins/owners manage members, invitations, and upstream configurations.

Password management, MFA, and email verification all live in Zitadel's hosted UI — the portal links out to it rather than reimplementing it.

The SPA is **embedded** in the binary via `embed.FS`. There is no separate static-asset server, no Node runtime in production, no Docker volume mount required.

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

  // Admin (admin + owner)
  rpc CreateUpstream(CreateUpstreamRequest) returns (CreateUpstreamResponse);
  rpc UpdateUpstream(UpdateUpstreamRequest) returns (UpdateUpstreamResponse);
  rpc DeleteUpstream(DeleteUpstreamRequest) returns (DeleteUpstreamResponse);
  rpc ListMembers(ListMembersRequest) returns (ListMembersResponse);
  rpc InviteMember(InviteMemberRequest) returns (InviteMemberResponse);  // → Zitadel UserService.AddHumanUser + AddUserGrant + CreateInviteCode
  rpc ResendInvite(ResendInviteRequest) returns (ResendInviteResponse);   // → Zitadel UserService.ResendInviteCode
  rpc UpdateMemberRole(UpdateMemberRoleRequest) returns (UpdateMemberRoleResponse);  // → Zitadel UserService.UpdateUserGrant
  rpc RemoveMember(RemoveMemberRequest) returns (RemoveMemberResponse);             // → Zitadel UserService.RemoveUserGrant + (optional) DeactivateUser

  // Owner-only
  rpc UpdateTenantSettings(UpdateTenantSettingsRequest) returns (UpdateTenantSettingsResponse);
  rpc TransferOwnership(TransferOwnershipRequest) returns (TransferOwnershipResponse);  // grant `owner` to target, demote previous owner — both via UpdateUserGrant
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
├── upstreams.go       // ListUpstreams, CreateUpstream, ...
├── members.go         // ListMembers (joins Limen users with Zitadel grants), InviteMember (→ AddHumanUser+AddUserGrant+CreateInviteCode), ResendInvite, UpdateMemberRole (→ UpdateUserGrant), RemoveMember (→ RemoveUserGrant)
├── mcpclients.go      // ListMCPClients, RevokeMCPClient
├── settings.go        // UpdateTenantSettings, TransferOwnership
└── errors.go          // Connect error mapping
```

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

    "CreateUpstream":      RoleAdmin,
    "UpdateUpstream":      RoleAdmin,
    "DeleteUpstream":      RoleAdmin,
    "ListMembers":         RoleAdmin,
    "InviteMember":        RoleAdmin,
    "ResendInvite":        RoleAdmin,
    "UpdateMemberRole":    RoleAdmin,
    "RemoveMember":        RoleAdmin,

    "UpdateTenantSettings":RoleOwner,
    "TransferOwnership":   RoleOwner,
}
```

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
    │   ├── Members.vue
    │   ├── MCPClients.vue
    │   └── Settings.vue
    └── components/
        ├── UpstreamCard.vue
        ├── MemberRow.vue
        └── …
```

Stack:

- **Vue 3** with Composition API + `<script setup>`.
- **Vite** for dev/build.
- **Pinia** for state (`session`, `upstreams`).
- **Vue Router** for `/login`, `/`, `/upstreams`, `/members`, `/mcp-clients`, `/settings`. Base path is `/t/<slug>/portal`; resolved at boot from `window.location.pathname`.
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

### Build & embed

- `pnpm install && pnpm build` in `web/` outputs to `web/dist/`.
- Go side embeds via `//go:embed web/dist/*` in a small `internal/portal/spa.go`.
- A handler serves the SPA: any GET under `/t/{tenant}/portal/` that doesn't match an API or static-asset route returns `index.html` (SPA fallback). Asset paths like `/portal/assets/*` are served directly with cache headers.
- The `index.html` is rewritten on-the-fly to inject `<base href="/t/{tenant}/portal/">` so the SPA's relative imports resolve correctly. (Alternative: build with a `BASE_URL` placeholder and substitute at request time.)

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
- New `internal/portal/` package with interceptors, service handlers, embed glue.
- New `web/` directory with the Vue 3 SPA.
- Updated `internal/transport/http.go` to mount `/t/{tenant}/api/...` and `/t/{tenant}/portal/...`.
- Updated `AGENTS.md` build section (Phase 10).
- `web/dist/.gitkeep` (or `.gitignore` of `web/dist/`, with CI building it).

## Security & operational notes

- **No `tenant_id` in request payloads** — interceptor is the single source of truth. A test verifies this; add a lint rule (`grep` in CI) if practical.
- **`session` ctx propagates RLS** — every RPC handler calls `storage.Session(ctx)` and lets RLS enforce isolation. No raw DB use.
- **Owner-protected operations** (`UpdateMemberRole`, `RemoveMember`, `TransferOwnership`) call Zitadel to list current user grants in the tenant org, then refuse the change if it would leave zero `owner` grants. Limen never trusts its DB for this — Zitadel is the source of truth.
- **MCP client revocation** calls Zitadel's Management API to delete the OIDC app, then removes the Limen `ZitadelApp` mirror row. Zitadel takes care of invalidating outstanding tokens issued for that client.
- **Consent** is shown by Zitadel's hosted UI (configurable per project) — no Limen-rendered consent screen.
- **CSP**: serve the SPA with `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'` and no eval. Tailwind plays fine with this.

## Verification

- `buf generate` produces compilable Go + TS.
- `go build ./...` clean.
- `pnpm build` clean; embed step picks up generated assets.
- Connect handlers respond correctly to:
  - Missing session → `unauthenticated`.
  - Wrong role → `permission_denied`.
  - Cross-tenant request (slug A cookie used on slug B URL) → `unauthenticated`.
  - Successful happy paths for each RPC.
- Cypress / Playwright smoke (optional in v1) covering: login → connect Atlassian upstream (mocked) → tool visible → disconnect.

## Risks

- **Embedding generated assets in the repo vs. building in CI**: pick one and document it in `AGENTS.md`. Recommendation: build in CI, do not commit `web/dist/`.
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
- [ ] `InviteMember` / `UpdateMemberRole` / `RemoveMember` / `TransferOwnership` mutate Zitadel user grants (no role column in Limen); zero-owner state is refused by listing grants in the tenant org first
- [ ] No `tenant_id` in request messages anywhere in the proto
- [ ] Vue 3 + Vite + Pinia + Vue Router + `@connectrpc/connect-web` SPA scaffolded under `web/`
- [ ] Pages: Login, Dashboard, Upstreams, Members, MCP Clients, Settings, Consent
- [ ] SPA base path resolved at boot from `/t/<slug>/portal/`
- [ ] Login flow uses classic POST + cookie (no JSON), CSRF via double-submit cookie
- [ ] Built SPA embedded via `//go:embed web/dist/*`
- [ ] SPA fallback handler returns `index.html` for unknown `/t/<slug>/portal/*` paths
- [ ] CSP header set on portal HTML responses
- [ ] `AGENTS.md` build section updated with `buf generate` and `pnpm build`
- [ ] CI does not commit `web/dist/` (or commits it deliberately — decision documented)
- [ ] Unit tests for the role-enforcement interceptor (matrix of method × role)
- [ ] Smoke test or manual run-through of the entire SPA flow
