# internal/portal — Connect-RPC portal service

This package implements the user-facing portal API as a Connect-RPC
service generated from `proto/limen/portal/v1/portal.proto`. The HTTP
binding lives at `/t/{tenant}/api/limen.portal.v1.PortalService/{Method}`
in the `portal` binary.

## Interceptor stack (in order)

1. **`tenancyInterceptor`** — confirms a `*storage.Tenant` is bound to
   ctx by the outer `tenancy.RequireTenant` HTTP middleware. Defense in
   depth: missing tenant means the mount is misconfigured, surfaced as
   `CodeNotFound`.
2. **`sessionInterceptor`** — decrypts the `limen_portal` cookie,
   verifies the Zitadel ID token, transparently refreshes on expiry,
   and pins `*UserSession` on ctx. **Skipped for `GetSession`** — that
   RPC is the SPA's "do I already have a session?" probe and runs
   without auth.
3. **`roleInterceptor`** — looks up the required role in `roles.go`'s
   `requiredRole` table and enforces it against the project-roles claim.
   **Unknown methods default-deny** — adding a new RPC without a row in
   `requiredRole` will return `CodePermissionDenied`.

## Conventions

- **Tenant is never in the request payload.** It is always sourced from
  the URL via `tenancy.MustTenant(ctx)`. The IDL-side enforcement lives
  in `internal/portal/portalv1guard`.
- **Identity is Zitadel-owned.** `UserSession` is built from verified
  ID-token claims; we do not write to a Limen-side identity table from
  this package. Limen-side `storage.User` rows are upserted by the OIDC
  callback in `internal/transport/portal.go`.
- **Handlers call `storage.Session(ctx)`.** RLS is bound by the
  tenancy-pinned ctx; never bypass with `WithSuperuser` from a portal
  RPC.
- **Errors flow through `errors.go`.** Internal errors are wrapped via
  `errInternal` which hides the cause from the wire — log the cause at
  the call site.
- **Cross-tenant cookie replay is killed at the AEAD layer.** The
  cookie's AAD includes the URL-derived tenant public id; a cookie
  minted for tenant A fails to decrypt under tenant B.

## Phase 7 boundary

The portal mounts only the user-facing surface. Outbound MCP traffic,
catalog enrichment, and code-mode dispatching belong to
`internal/gateway` and are unaffected by anything in this package.

## Wiring

`internal/boot/portalmount.Mount` is the only place that constructs
`*portal.Service` and hands it to `transport.MountPortal` via
`PortalDeps.ConnectAPI`. The `cmd/gateway` and `cmd/staff` binaries
explicitly exclude this package via their `import_graph_test.go`.
