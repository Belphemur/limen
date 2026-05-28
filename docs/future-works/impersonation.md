---
status: planned
created: "2026-05-27"
---

# User-to-User Impersonation

> **Future capability** — evaluated for Phase 9k, rejected in favor of direct admin RPCs. This document captures the design for when impersonation is eventually needed (backoffice support, owner debugging, or Phase 12 staff backoffice).

## Goal

Allow an admin or owner to temporarily act as another identity -- human user or service account -- to diagnose linking issues, verify MCP tool access, or provide support. The impersonated session behaves identically to a real login: the same RPCs, the same upstream links, the same role checks. The only difference is an audit trail field (`ImpersonatedBy`) that records who initiated the impersonation.

## Architecture

### Resolver Wrapper

The core mechanism is an `ImpersonationResolver` that wraps the production `session.Resolver` (currently `session.OIDCResolver`):

```
Request → session.Interceptor → [ImpersonationResolver] → [OIDCResolver] → UserSession
```

ImpersonationResolver checks for the `limen_impersonate` cookie _before_ delegating to the inner resolver. Three outcomes:

| Scenario | Behaviour |
|----------|-----------|
| Valid impersonation cookie | Decrypt, validate PAT, build `UserSession` with SA/user identity + `ImpersonatedBy` |
| No impersonation cookie | Delegate to inner resolver (normal OIDC flow) |
| Invalid / expired cookie | **Delegate to inner resolver** -- do not error. The interceptor (`session.Interceptor`) treats any resolver error as unauthenticated (returns `errUnauthenticated`). Failing open preserves the admin's real session. |

```
┌─────────────────────────────────────────────────────────────────┐
│  session.Interceptor                                            │
│                                                                 │
│    ┌─ resolve(ctx, header, tenant) ───────────────────────────┐ │
│    │                                                          │ │
│    │  ┌──────────────────────────────────────────────────┐    │ │
│    │  │ ImpersonationResolver                             │    │ │
│    │  │                                                   │    │ │
│    │  │  1. Check limen_impersonate cookie                │    │ │
│    │  │  2a. Valid → decrypt, validate PAT,               │    │ │
│    │  │      return UserSession{... , ImpersonatedBy: ...}│    │ │
│    │  │  2b. Absent → delegate to inner                   │    │ │
│    │  │  2c. Invalid → delegate to inner (fail open)      │    │ │
│    │  │                                                   │    │ │
│    │  │  ┌───────────────────────────┐                    │    │ │
│    │  │  │ inner (OIDCResolver)      │                    │    │ │
│    │  │  │   └→ ResolvePortalSession │                    │    │ │
│    │  │  └───────────────────────────┘                    │    │ │
│    │  └──────────────────────────────────────────────────┘    │ │
│    │                                                          │ │
│    └──────────────────────────────────────────────────────────┘ │
│                                                                 │
│    if err != nil || sess == nil → return errUnauthenticated     │
│    next(WithUser(ctx, sess))                                    │
└─────────────────────────────────────────────────────────────────┘
```

### `UserSession` Extension

`UserSession` gains an optional `ImpersonatedBy` field:

```go
type UserSession struct {
    Subject         string
    Email           string
    FirstName       string
    LastName        string
    Roles           []string
    AccessToken     string
    ImpersonatedBy  string // "" when not impersonating; admin's subject when impersonating
}
```

Handlers can check `sess.ImpersonatedBy != ""` to display a banner or adjust behaviour (e.g. disable destructive actions).

### Wiring

In `internal/boot/portalmount/portalmount.go`, the resolver wire changes from:

```go
resolver := session.OIDCResolver(oidc)
```

to:

```go
innerResolver := session.OIDCResolver(oidc)
resolver := session.ImpersonationResolver(innerResolver, oidc, rt.Store, rt.Logger)
```

`ImpersonationResolver` returns a `Resolver` (same function type), so no changes are needed in `session.Interceptor` or any service constructor.

## Cookie Design

| Attribute | Value |
|-----------|-------|
| Name | `limen_impersonate` |
| Encryption | AES-SIV (same `*crypto.Cipher` as `limen_portal`) |
| AAD | `{TenantID: tenant, Kind: "impersonation.cookie"}` |
| Path | `/t/{tenant}` |
| HttpOnly | `true` |
| Secure | `true` (prod) |
| SameSite | `Lax` |
| TTL | 15 minutes (enforced server-side, not just `MaxAge`) |

### Payload Structure

```go
type ImpersonationCookieValue struct {
    PAT             string    // short-lived Zitadel PAT (sealed)
    SAPublicID      string    // target service account public id (or user subject)
    ImpersonatorSub string    // admin's Zitadel subject
    ImpersonatorName string   // "First Last" for audit display
    UserType        uint8     // 0 = human user, 1 = service account
    ExpiresAt       time.Time // server-side expiry (15 min from mint)
}
```

Binary format: same `uint16` length-prefix scheme as `PackPortalCookie`, then zstd → AES-SIV → base64.

### Cookie Lifecycle

1. **Start**: Admin calls `ImpersonateServiceAccount` RPC → backend mints PAT → seals into cookie → `Set-Cookie: limen_impersonate` on response.
2. **Active**: Every Connect-RPC call carries the cookie alongside `limen_portal`. `ImpersonationResolver` picks it up.
3. **Stop**: Client calls `StopImpersonation` RPC → backend sends `Set-Cookie: limen_impersonate=; MaxAge=-1`. JS cannot delete `HttpOnly` cookies, so an RPC endpoint is required.
4. **Expiry**: After 15 minutes, the resolver's server-side check on `ExpiresAt` causes it to fall through to the inner resolver, restoring the admin's real session.

## PAT as Impersonation Credential

The impersonation cookie carries a Zitadel Personal Access Token (PAT) as the authenticated credential for the impersonated identity:

1. Admin calls `ImpersonateServiceAccount(rpc)` with `service_account_public_id`.
2. Backend looks up the SA → gets the underlying Zitadel `user_id`.
3. Calls `*zitadel.Client.AddPersonalAccessToken(ctx, zitadelUserID, expiry_15min)` → receives raw PAT string.
4. Seals the PAT + metadata into the impersonation cookie.

On each request, the resolver:

1. Decrypts the cookie, extracts the PAT.
2. Validates the PAT against Zitadel's JWKS (same `rp.VerifyIDToken` path as normal session resolution, but with the PAT as a bearer token).
3. Looks up the SA in the local DB (defense: SA might have been deleted between impersonation start and the request).
4. Builds a `UserSession` with the SA's identity, populating `ImpersonatedBy` with the admin's subject.

This means the impersonated request is cryptographically bound to a real Zitadel credential with a hard expiry -- no custom token format or bespoke validation logic.

## Security Constraints

| Constraint | Enforcement |
|------------|-------------|
| **Role gate**: only `owner` or `admin` can start impersonation | Checked in the `ImpersonateServiceAccount` RPC handler via `RoleInterceptor` |
| **No chaining**: an already-impersonated session cannot impersonate again | `ImpersonateServiceAccount` rejects if `UserFromContext(ctx).ImpersonatedBy != ""` |
| **Audit trail**: every start/stop logged with correlation ID | `zap.Info` with fields: `impersonator`, `target`, `target_type`, `correlation_id` |
| **Auto-expiry**: Zitadel PAT expires after 15 minutes; server-side `ExpiresAt` check enforces the same window | No cleanup cron needed -- Zitadel rotates the token, stale cookies decrypt to a `nil` user and fall through |
| **Session isolation**: the admin's real session (`limen_portal` cookie) is preserved throughout and restored when impersonation ends | Two independent cookies, same path -- `ImpersonationResolver` reads `limen_impersonate` first, `OIDCResolver` reads `limen_portal` only as fallback |
| **Tenant binding**: AAD includes tenant -- a cookie minted for tenant A cannot be decrypted under tenant B | Same defense as `limen_portal` |

## Why This Was Rejected for Phase 9k

Phase 9k (SA upstream linking) evaluated two approaches:

| Criteria | Impersonation (A) | Direct Admin RPCs (B) [chosen] |
|----------|-------------------|-------------------------------|
| Lines of code | ~560 | ~460 |
| Files touched | 9+ | 6 |
| Novel auth concepts | Impersonation cookie system, resolver wrapper, PAT-as-session | None -- delegates to existing `upstream.Service` |
| Portal handler changes | Yes -- `callerContext` needs SA awareness during impersonation | No -- admin RPCs build `LinkContext` with `ServiceAccountID` |
| OAuth callback compatibility | No -- callback runs at chi middleware level, not through Connect-RPC interceptors; impersonation resolver would not fire | Yes -- callback resolves admin normally, SA targeting happens inside `FinishLink` via envelope metadata |
| Blast radius | Auth boundary (cookie format, resolver, interceptor wiring) | Strategy layer + 4 admin RPCs |

**Decision: Approach B (direct admin RPCs).** Fewer files, fewer novel concepts, no changes to the portal SPA or the OAuth callback path. SA-awareness is isolated to `LinkContext.ServiceAccountID` and `OwnerIDStr()` -- a single additive field that the existing strategy layer reads.

The impersonation design is preserved here for when the use cases demand it (see below).

## Use Cases for Future Implementation

### Backoffice Support (Portal + Admin)

An admin views the portal as a specific user to diagnose linking issues. The admin sees exactly what the user sees: the same upstreams with the same link states, the same tool availability. An impersonation banner indicates who they are acting as.

### Owner Debugging

A tenant owner impersonates a service account to test MCP tool access through the gateway. This validates that the SA's PAT correctly resolves SA-owned upstream links and that tool calls succeed with the right credentials.

### Staff Backoffice (Phase 12)

Limen staff impersonates tenant admins for support. This requires a cross-tenant impersonation cookie (`Path=/t/` instead of `Path=/t/{tenant}`) so a single impersonation session spans multiple tenants. Phase 12 will reuse the `ImpersonationCookieValue` format with `UserType=0` (real user) and staff-specific additions.

## Integration Points

| Package | File | Change |
|---------|------|--------|
| `internal/session/` | `context.go` | Add `ImpersonatedBy string` to `UserSession`; add `ImpersonationResolver(inner, auth, store, logger) Resolver` |
| `internal/session/` | `interceptor.go` | No changes -- resolver wrapper is transparent to the interceptor |
| `internal/boot/portalmount/` | `portalmount.go` | Wrap `session.OIDCResolver(oidc)` with `session.ImpersonationResolver(...)` |
| `internal/auth/` | `oidc.go` | Add `limen_impersonate` cookie name constant; `ReadImpersonationCookie` method on `*OIDC`; `AAD` Kind constant `"impersonation.cookie"` |
| `internal/crypto/` | `aessiv.go` | No changes -- reuses existing `*Cipher` |
| `internal/zitadel/` | `service_accounts.go` | No changes -- `AddPersonalAccessToken` already exists |
| `internal/admin/` | `service_accounts.go` | Add `ImpersonateServiceAccount` and `StopImpersonation` RPC handlers |
| `proto/limen/admin/v1/` | `admin.proto` | Add `ImpersonateServiceAccount` and `StopImpersonation` RPCs + messages |
| `proto/limen/session/v1/` | `session.proto` | Add `ImpersonatedBy` to `GetSessionResponse` (or a dedicated `ImpersonationInfo` message) |

## Risks & Mitigations

| Risk | Severity | Mitigation |
|------|----------|------------|
| **OAuth callback incompatibility**: The OAuth callback handler at `transport/upstream.go` runs at chi middleware level, not through Connect-RPC interceptors. The `ImpersonationResolver` would not fire for callback requests, so an admin mid-impersonation who kicks off an OAuth flow would lose impersonation context. | High | Document this as a known limitation; require exiting impersonation before OAuth flows. Alternatively, add impersonation check to the OAuth middleware stack. |
| **PAT revocation gap**: If an admin's Zitadel permissions are revoked mid-impersonation, the already-minted PAT remains valid until expiry (up to 15 minutes). | Low | 15-minute window is acceptable. For stricter scenarios, PAT validity can be shortened or validated against a server-side allowlist. |
| **Cookie confusion**: Two `HttpOnly` cookies on the same path (`limen_portal` and `limen_impersonate`) might confuse developers debugging auth issues. | Low | Clear naming convention and documentation. The resolver's fail-open design means impersonation cookie issues never break normal auth. |
| **Audit compliance**: Impersonation accesses another user's data; must be auditable for compliance. | Medium | Every impersonation start/stop is logged with correlation ID. The `ImpersonatedBy` field on `UserSession` is available to all handlers for audit context. Consider adding an `audit` event table (planned for Phase 12). |
| **Portal SPA awareness**: The portal SPA must display an impersonation banner and provide a "Stop impersonating" button. Requires changes to `web/shared/src/session/` and `web/portal/src/App.vue`. | Medium | Design shared banner in `web/shared/` (reused by admin SPA and phase 12 staff SPA). Banner renders when `GetSessionResponse.ImpersonatedBy` is non-empty. |
