---
phase: "13d"
title: "Plan Enforcement"
status: completed
progress: 95
updated: "2026-06-14"
---

# Phase 13d — Plan Enforcement

**Depends on**: Phase 13c (Stripe Integration), Phase 13b (billing metrics pipeline).
**Unblocks**: Phase 13e (portal billing page).

## Goal

Enforce subscription entitlements at runtime — prevent non-paying tenants from using features they haven't paid for. Add a BillingInterceptor that loads entitlements into the request context, feature-gate checks at critical RPC endpoints, and a Valkey-backed entitlement cache with webhook-triggered invalidation.

## Design

### Architecture

The enforcement system sits between authentication and authorization in the Connect-RPC interceptor chain:

```
TenancyInterceptor → BearerTokenInterceptor → session.Interceptor → BillingInterceptor → session.RoleInterceptor → handler
```

The **BillingInterceptor** loads the tenant's entitlements (from cache or database) and injects them into the request context. Individual RPC handlers then check entitlements via guard clauses before performing gated operations.

### Entitlement Cache

- **Storage**: Valkey (key: `limen:billing:entitlements:{tenantID}`)
- **TTL**: 5 minutes
- **Serialization**: JSON
- **Fallback**: Database query via `EntitlementsFromRows()`
- **Invalidation**: On Stripe webhook `entitlements.active_entitlement_summary.updated`

### Enforcement Flow

```
Request arrives
    → tenancy.RequireTenant resolves tenant
    → session.Interceptor authenticates user
    → BillingInterceptor:
        ├─ Cache hit → use cached PlanEntitlements
        └─ Cache miss → DB query → populate cache
    → Injected into context via WithEntitlements(ctx, ents)
    → Handler: ents, ok := EntitlementsFromContext(ctx)
        └─ if ok && limit exceeded → return connect.CodePermissionDenied
```

### Structured Errors

Errors carry machine-readable detail the portal frontend can use for upgrade prompts:

```go
type ErrFeatureLocked struct {
    Feature string // "max-users", "max-sa-connections", etc.
    Limit   int32  // the limit (-1 for boolean features)
    Usage   int32  // current usage
    Message string // human-readable
}
```

Error format: `billing.limit.{feature}: {message} (limit=X, usage=Y)`

RPCs return `connect.CodePermissionDenied` when a feature gate blocks the operation.

## Deliverables

### New Package: `internal/billing/enforcer/`

| File | Purpose |
|------|---------|
| `context.go` | `WithEntitlements` / `EntitlementsFromContext` context injection |
| `cache.go` | Valkey-backed TTL cache with DB fallback, JSON serialization |
| `enforcer.go` | `ForTenant()` cache-first lookup, `Invalidate()` for webhook |
| `errors.go` | `ErrFeatureLocked` structured error type |
| `gates.go` | `CheckMaxUsers`, `CheckMaxSAConnections`, `CheckMaxProjects`, `CheckStorageLimit`, `CheckFeature` |
| `interceptor.go` | `BillingInterceptor` Connect-RPC unary interceptor |
| `lifecycle.go` | `RequireBillingActive` HTTP middleware + pure `evaluateBillingStatus` state machine (`decisionPass` / `decisionPassGrace` / `decisionBlock` / `decisionPassUnknown`) |
| `enforcer_test.go` | 23 unit tests covering context round-trip, cache, gates, errors, interceptor |
| `lifecycle_test.go` | 18 table-driven cases for `evaluateBillingStatus` (every Stripe status, grace-window edge cases, unknown-status fail-open) |

### Wired Enforcement Points (complete)

| RPC | File | Feature | Behavior |
|-----|------|---------|----------|
| `InviteMember` | `admin/members.go` | `max-users` | Rejects if active users ≥ MaxActiveUsers |
| `CreateServiceAccount` | `admin/service_accounts.go` | `max-service-accounts` | Rejects if service accounts ≥ MaxServiceAccounts |
| `CallTool` | `gateway/manager.go` | `max-sa-connections` | Soft in-band MCP error if active SA links ≥ MaxSAConnections |

### Wired Infrastructure

| Component | File | Change |
|-----------|------|--------|
| Enforcer creation | `boot/billingmount/billingmount.go` | Creates `enforcer.New()`, adds to Dependencies |
| Interceptor wiring | `admin/service.go` | Added BillingInterceptor after session auth |
| Lifecycle middleware wiring | `internal/transport/mcprs.go` + `internal/transport/portal.go` | `RequireBillingActive` mounted on MCP auth group and `/t/{tenant}/api/*` (BillingService prefix exempt) |
| Lifecycle middleware construction | `boot/servegateway/`, `boot/serveportal/`, `boot/serveall/` | `RequireBillingActive` built per-binary and passed into `mcpmount.Mount` / `portalmount.Mount` |
| Serve binaries | `serveportal/serveportal.go`, `serveall/serveall.go` | Reordered billingmount before portalmount |
| Webhook invalidation | `billing/stripe/webhook.go` | `Invalidate()` called after entitlement upsert |

### Deferred Enforcement Points (TODO markers in code)

| Feature | File | Reason |
|---------|------|--------|
| `code-mode` | `transport/codemode_server.go` | Needs MCP protocol integration for error surfacing |
| `advanced-ai` | `gateway/manager.go` | Needs model-tier routing logic; basic-ai always allowed |
| `sso` | `admin/settings.go` | Needs UI integration for hiding/configuring SSO |
| `audit-logs` | `audit/audit.go` | Audit emitter is fire-and-forget; cleanest to gate at Emit |
| `max-projects` | `zitadel/projects.go` | Cross-package concern; caller should check before calling EnsureProject |
| `max-storage` | (no upload endpoint yet) | File upload/storage not yet implemented |

## Verification

- **Unit tests**: 23 enforcer + 18 lifecycle = 41/41 passing in `internal/billing/enforcer/`
- **Import graph**: `cmd/gateway` continues to exclude `internal/oauthproxy`, `internal/zitadel`, `internal/portal`, `internal/admin`, `internal/signup` — the lifecycle middleware lives in `internal/billing/enforcer/` (a subpackage of `internal/billing`) so the gateway can import it without pulling the Stripe service that depends on `internal/portal/portalv1`.
- **Integration**: Enforcement tests require a running instance; deferred to CI
- **Manual**: Invite a 2nd member on Developer plan → expect `CodePermission PermissionDenied` with `billing.limit.max-users`
