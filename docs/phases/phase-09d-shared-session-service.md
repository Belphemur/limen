# Phase 9d — Shared `SessionService` (DRY the session)

**Depends on**: Phases 4 (portal cookie + role claim), 9b (portal SPA scaffold), 9c (admin SPA scaffold)
**Unblocks**: 9b refactor (drop `PortalService.GetSession`), 9c session integration, 12 (staff SPA reuses the same RPC)

## Goal

Both SPAs — customer portal ([Phase 9b](phase-09b-portal-spa.md)) and tenant admin ([Phase 9c](phase-09c-tenant-admin-spa.md)) — need exactly one thing on boot: **decode the Phase 4 portal cookie and return `{ tenant, user, role }`**. Putting `GetSession` on both `PortalService` and `AdminService` duplicates the proto, the handler, the TS client, and the SPA session store.

This phase consolidates that into one tiny shared service and one shared TS module. **No new product capability** — pure DRY / SOLID / KISS pass on the session boundary.

> **Development posture**: this project is in **full development mode**. Breaking changes are accepted and expected. There are no migration shims, no compatibility aliases, no transitional `GetSession`-on-both-services period. The cutover lands in one commit, both SPAs flip together, and the old proto methods are deleted.

## Design

### Why a third service (not "reuse `PortalService` from admin")

| Option                                                               | Verdict                                                                                                                                                                                                                                                                                                            |
| -------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Keep `GetSession` on `PortalService`, admin SPA imports portal proto | Rejected. Semantic mismatch — "PortalService" reads as user-facing, calling it from the admin SPA confuses the next reader. The role interceptors differ (portal accepts `member`; admin requires `owner`/`admin`); sharing the proto would force a `RoleAny` carve-out on a service whose name implies otherwise. |
| Duplicate `GetSession` on both services                              | Rejected. Two proto definitions, two Go handlers, two TS clients, two SPA stores — all returning identical bytes. Pure copy-paste.                                                                                                                                                                                 |
| **New `limen.session.v1.SessionService`**                            | **Accepted.** One service, one handler, one TS client, one SPA store. Different interceptor stack from `PortalService`/`AdminService` (it accepts any authenticated cookie, no role check), which makes the separation honest.                                                                                     |

The new package is intentionally tiny — exactly one RPC. Adding a second RPC requires a real second responsibility, not "we already had a service file open."

### `proto/limen/session/v1/session.proto`

```proto
syntax = "proto3";
package limen.session.v1;

service SessionService {
  // Decode the portal cookie placed by Phase 4 and return the
  // caller's identity facts. Called by every SPA on boot.
  rpc GetSession(GetSessionRequest) returns (GetSessionResponse);
}

message GetSessionRequest {}

message GetSessionResponse {
  Tenant tenant = 1;
  User   user   = 2;
  Role   role   = 3;
}

message Tenant {
  string public_id = 1;   // "tnt_<ULID>"
  string name      = 2;
}

message User {
  string id         = 1;   // Zitadel subject
  string email      = 2;
  string first_name = 3;
  string last_name  = 4;
}

enum Role {
  ROLE_UNSPECIFIED = 0;
  ROLE_MEMBER      = 1;
  ROLE_ADMIN       = 2;
  ROLE_OWNER       = 3;
}
```

Request body is empty: the tenant comes from the URL path, the user identity comes from the cookie. Phase 9b's "no `tenant_id` in payloads" invariant is preserved.

### `internal/session/`

```
internal/session/
├── service.go         // implements SessionServiceHandler — ~30 lines
├── mount.go           // boot helper that wires the handler into chi
└── service_test.go    // table-driven: cookie present → response; absent → unauthenticated
```

The handler is a passthrough over what the existing `tenancyInterceptor + portalSessionInterceptor` already place in the request context. It does **not** call Zitadel; that's the interceptor's job (cached 60 s per Phase 9b). Concretely:

```go
func (s *Service) GetSession(ctx context.Context, _ *connect.Request[sessionv1.GetSessionRequest]) (*connect.Response[sessionv1.GetSessionResponse], error) {
    tnt := tenancy.MustFromContext(ctx)
    usr := auth.MustUserFromContext(ctx)
    return connect.NewResponse(&sessionv1.GetSessionResponse{
        Tenant: toTenantPB(tnt),
        User:   toUserPB(usr),
        Role:   toRolePB(usr.HighestRole()),
    }), nil
}
```

Mounted with **only** the two infrastructural interceptors:

| Interceptor                | Action                                                                |
| -------------------------- | --------------------------------------------------------------------- |
| `tenancyInterceptor`       | Resolve `{tenant}` from URL → `*Tenant` in ctx                        |
| `portalSessionInterceptor` | Decrypt cookie, validate, place `*User` + roles in ctx; 401 otherwise |

No `roleInterceptor` — `SessionService` is the one place where "any authenticated user" is the correct gate, because the caller doesn't yet know what they're allowed to do.

### Mounting

The handler is mounted once per binary that exposes a SPA-facing API. In the [Phase 9a](phase-09a-binary-split.md) split:

- `cmd/portal` → mounts `SessionService` + `PortalService` + `AdminService` under `/t/{tenant}/api/`.
- `cmd/staff` → mounts `SessionService` + `StaffService` under `/t/_staff/api/` (Phase 12 inherits this for free).
- `cmd/gateway` → does **not** mount it. Gateway is bearer-token only; no cookies.
- `cmd/limen` (all-in-one) → mounts everything.

`internal/boot/portalmount/` gains a one-line `mountSession(r, deps)` call. Same shape as the existing `mountPortal`, `mountAdmin` helpers.

### Frontend — `web/shared/`

A new pnpm workspace package consumed by every SPA. The package is intentionally tiny:

```
web/shared/
├── package.json                  # name: "@limen/shared", private, no build step
├── tsconfig.json                 # extended by each SPA
├── src/
│   ├── gen/session/v1/           # buf-generated TS (gitignored)
│   ├── session/
│   │   ├── store.ts              # Pinia store: bootstrap, refresh, logout, error kinds
│   │   ├── client.ts             # createSessionClient(transport) factory
│   │   └── routerGuard.ts        # createSessionGuard(router, store, opts)
│   └── index.ts                  # barrel
```

#### `store.ts` — single Pinia store, both SPAs import it

`useSessionStore()` exposes exactly what both current implementations expose (`loaded`, `tenant`, `user`, `role`, `error`, `bootstrap`, `refresh`, `logout`). The signature is locked here; new SPAs cannot redefine it.

- `bootstrap()` — idempotent, dedupes concurrent calls via `inFlight`. Used by the router guard.
- `refresh()` — always re-fetches. Used after explicit user actions.
- `logout()` — hard-redirects to `${tenantPrefix}/auth/logout`. Tenant prefix is derived from `window.location.pathname` matching `/t/<tenant>/`.

#### `routerGuard.ts` — single guard, both SPAs install it

```ts
export function createSessionGuard(
  router: Router,
  options: {
    publicRouteFlag?: string; // route.meta key for opt-out, default "public"
    loginUrl: (returnTo: string) => string; // tenant-prefixed /auth/login builder
    forbiddenRouteName: string; // name of the "forbidden" route
  },
): void {
  router.beforeEach(async (to) => {
    if (to.meta[options.publicRouteFlag ?? "public"] === true) return true;
    const session = useSessionStore();
    await session.bootstrap();
    if (session.error === "unauthenticated") {
      window.location.replace(options.loginUrl(to.fullPath));
      return false;
    }
    if (session.error === "permission_denied") {
      return { name: options.forbiddenRouteName };
    }
    return true;
  });
}
```

Each SPA passes its own `loginUrl` builder (they differ only in path prefix). The guard logic itself is shared.

#### `pnpm-workspace.yaml`

```yaml
packages:
  - web/portal
  - web/admin
  - web/shared
```

Workspace dependency in each SPA: `"@limen/shared": "workspace:*"`.

### Codegen

`buf.gen.yaml` adds one output line for the new package:

- Go: `internal/portal/sessionv1/` (or `internal/session/v1/` — pick one and document it; current convention is `internal/<service>/<package>v1/`, so `internal/session/sessionv1/`).
- TS: `web/shared/src/gen/session/v1/`.

Both SPAs continue to generate their service-specific TS into their own `src/gen/` — only the session client lives in shared.

## What gets deleted (breaking changes)

Full dev mode → no deprecation period. The same PR that lands `SessionService` removes:

| Removed                                                                               | Reason                                                                                                                                   |
| ------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `PortalService.GetSession` (proto, handler, role map row, doc references)             | Replaced by `SessionService.GetSession`.                                                                                                 |
| `web/portal/src/stores/session.ts`                                                    | Re-exported from `@limen/shared`.                                                                                                        |
| `web/admin/src/stores/session.ts`                                                     | Re-exported from `@limen/shared`.                                                                                                        |
| `web/admin/src/transport/adminClient.ts::getSession()` and the `SessionResponse` type | Lives on `@limen/shared`'s session client. The remaining `adminClient` only hosts admin-specific RPCs (upstream CRUD, settings, signup). |
| `web/admin/src/transport/authError.ts` (if duplicated in `web/portal/src/transport/`) | Lifted into `@limen/shared/src/session/`.                                                                                                |

The phase-9b doc's "the only RPC that does not require an authenticated session" bullet flips to "**SessionService.GetSession** is the only RPC that does not require an authenticated session." Same behaviour, different package.

## Deliverables

- `proto/limen/session/v1/session.proto` — `SessionService` + messages.
- `internal/session/` — handler + mount helper + tests.
- `buf.gen.yaml` updated; `buf generate` produces Go + TS bindings.
- `web/shared/` — new pnpm workspace package; session store + client factory + router guard.
- `web/portal/` and `web/admin/` migrated to `@limen/shared`. Duplicated session modules deleted.
- `pnpm-workspace.yaml` lists `web/shared`.
- `internal/boot/portalmount/`, `internal/boot/serveportal/`, `internal/boot/servestaff/`, `internal/boot/serveall/` mount the new service.
- Phase 9b doc updated: `PortalService.GetSession` removed; pointer to this phase added.
- Phase 9c doc updated: PortalService "kept methods" list drops `GetSession`; pointer to this phase added.
- Phase 12 doc updated: the staff SPA boot flow references `SessionService.GetSession` instead of any staff-local equivalent.

## Verification

- `go test ./internal/session/...` — handler returns the cookie-derived identity; missing/expired cookie returns `unauthenticated`.
- `pnpm -F @limen/shared test` — Pinia store unit tests: `bootstrap()` dedupes concurrent callers; `refresh()` always re-fetches; `logout()` resolves the correct tenant prefix.
- `pnpm -F @limen/portal test` and `pnpm -F @limen/admin test` — both SPAs boot through the shared guard against a stubbed `SessionService`; both redirect on `unauthenticated`; both route to `forbidden` on `permission_denied`.
- E2E: clean-cache visit to `/t/<tenant>/portal/` and `/t/<tenant>/admin/` both succeed with one identical `SessionService.GetSession` round-trip; DevTools network panel shows the call against `/t/<tenant>/api/limen.session.v1.SessionService/GetSession`.
- `grep -r "PortalService\.GetSession" .` returns zero matches in `internal/`, `web/`, and `proto/`.

## Risks

- **`web/shared` adoption friction**: pnpm workspace symlinks bite occasionally on Windows/WSL. Mitigation: CI runs the build on Linux only; document the workspace command in `AGENTS.md`.
- **Codegen target rename**: if we keep `PortalService.GetSession` accidentally (e.g. a forgotten generated TS file), the SPA could prefer it. The verification `grep` step is the canonical guard; add it to CI as `make lint` or equivalent.
- **Staff session shape divergence**: Phase 12 may want `factors.mfa.verified_at` exposed for the impersonation flow. That's an additive field on `GetSessionResponse`; design it now if Phase 12 lands before this one, otherwise add when needed.

## Checklist

- [ ] `proto/limen/session/v1/session.proto` defines `SessionService.GetSession` only
- [ ] `buf generate` produces Go bindings under `internal/session/sessionv1/` and TS under `web/shared/src/gen/session/v1/`
- [ ] `internal/session/` implements the handler (passthrough over context-bound user/tenant); no Zitadel call inside the handler
- [ ] Handler mounted via `tenancyInterceptor + portalSessionInterceptor` only — no `roleInterceptor`
- [ ] `cmd/portal`, `cmd/staff`, `cmd/limen` all mount `SessionService`; `cmd/gateway` does not
- [ ] `web/shared/` package created with session store + client factory + router guard
- [ ] `pnpm-workspace.yaml` lists `web/shared`
- [ ] `web/portal/` consumes `@limen/shared`; local `stores/session.ts` deleted
- [ ] `web/admin/` consumes `@limen/shared`; local `stores/session.ts` + `getSession`-related code in `adminClient.ts` deleted
- [ ] `PortalService.GetSession` removed from proto, handler, role map, and all doc references (Phase 9b + 9c updated)
- [ ] `grep -r "PortalService\.GetSession" .` returns zero results
- [ ] Both SPAs' bootstrap path uses `@limen/shared`'s `createSessionGuard`; no duplicate guard code
- [ ] Tests pass on both Go and pnpm sides; E2E confirms the shared endpoint is the only session round-trip
- [ ] `AGENTS.md` references the workspace structure (`web/shared/`)
