---
phase: "13d"
title: "Plan Enforcement"
status: completed
progress: 100
updated: "2026-06-26"
---

# Phase 13d — Plan Enforcement

**Depends on**: Phase 13c (Stripe Integration), Phase 13b (billing metrics pipeline).
**Unblocks**: Phase 13e (portal billing page — consumes the entitlement state for usage counters and the `X-Limen-Billing: grace` middleware header to drive the reactive `BillingBanner`).

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
| `lifecycle.go` | `RequireBillingActive` HTTP middleware + pure `evaluateBillingStatus` state machine. Verdict map: `canceled` / `past_due`-expired / `unpaid`-expired → `decisionPass` (triggers one-time auto-downgrade to Developer); `past_due` / `unpaid` within grace → `decisionPassGrace`; `incomplete` / `incomplete_expired` / `paused` → `decisionBlock`; unknown → `decisionPassUnknown` (fail-open with warning). `DeveloperPlan = "developer"` constant exported. |
| `downgrade.go` | Package-level `DowngradeToDeveloper(tx *gorm.DB, tenantID int64) error` — sets `Plan="developer"`, clears `GraceUntil`, hard-deletes `tenant_entitlements` via `Unscoped()`. Idempotent: no-op when `billing.Plan` is already `"developer"` or row missing. Caller manages the tx and invalidates the cache after commit. |
| `enforcer_test.go` | 31 unit tests covering context round-trip, cache, gates, errors, interceptor |
| `lifecycle_test.go` | 18 table-driven cases for `evaluateBillingStatus` (every Stripe status, grace-window edge cases incl. `now == grace` boundary, unknown-status fail-open) — the `(auto-downgrade)` suffix marks cases that now exercise the `decisionPass` + downgrade path |

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
| Lifecycle middleware signature | `internal/billing/enforcer/lifecycle.go` | `RequireBillingActive(store *storage.Store, enforcer *Enforcer, cfg config.BillingConfig, logger *zap.Logger)` — enforcer required (not optional) when `cfg.Enabled` is true so the auto-downgrade path can invalidate the entitlement cache after commit |
| Lifecycle middleware construction | `boot/servegateway/`, `boot/serveportal/`, `boot/serveall/` | `RequireBillingActive` built per-binary and passed into `mcpmount.Mount` / `portalmount.Mount`. `servegateway` constructs the `*Enforcer` inline (`enforcer.New(rt.Store, rt.Valkey, ...)`) — no `billingmount` import, preserving the `cmd/gateway` import-graph test |
| Serve binaries | `serveportal/serveportal.go`, `serveall/serveall.go` | Reordered billingmount before portalmount |
| Webhook invalidation | `billing/stripe/webhook.go` | `Invalidate()` called after entitlement upsert, and now also after `subscription.updated` and `subscription.deleted` (gated on commit success AND `billing.TenantID != 0`) so the cache reflects the post-cancelation state on the very next read |

### Deferred Enforcement Points (TODO markers in code)

| Feature | File | Reason |
|---------|------|--------|
| `code-mode` | `transport/codemode_server.go` | Needs MCP protocol integration for error surfacing |
| `advanced-ai` | `gateway/manager.go` | Needs model-tier routing logic; basic-ai always allowed |
| `sso` | `admin/settings.go` | Needs UI integration for hiding/configuring SSO |
| `audit-logs` | `audit/audit.go` | Audit emitter is fire-and-forget; cleanest to gate at Emit |
| `max-projects` | `zitadel/projects.go` | Cross-package concern; caller should check before calling EnsureProject |
| `max-storage` | (no upload endpoint yet) | File upload/storage not yet implemented |

### Auto-Downgrade (cancellation + expired-grace recovery)

The lifecycle state machine returns `decisionPass` for the Stripe statuses
that represent a *finished* subscription — `canceled`, and `past_due` /
`unpaid` whose grace window has expired (or was never set). Rather than
serving a 402 to a customer who has already lost their entitlement, the
middleware performs a **one-time, idempotent downgrade to the Developer
plan** in the same request and lets the request proceed with developer
limits. The status that 402s is now only the Stripe mid-flow states
(`incomplete`, `incomplete_expired`, `paused`) — the cases where the
customer must complete checkout / un-pause before they can come back.

**Why:** the previous design 402'd any request that hit a cancelled or
expired-grace tenant. In practice that means a customer cancels Stripe,
makes one more request to grab a tool result, and is met with a payment
wall for an account they've already given up. Auto-downgrade makes the
boundary match the product model: free tier is free.

**How:**

- The pure `evaluateBillingStatus` state machine lives in
  `lifecycle.go` and is exhaustively table-tested. The verdict map is
  documented in the function's doc comment. The grace comparison is
  strict-less-than: `now == graceUntil` is treated as expired and
  returns `decisionPass` (boundary pinned by
  `TestEvaluateBillingStatus_GraceBoundary`).
- The new package-level function
  `enforcer.DowngradeToDeveloper(tx *gorm.DB, tenantID int64) error` in
  `internal/billing/enforcer/downgrade.go` is the single source of
  truth for the write. It sets `tenant_billing.plan = "developer"`,
  clears `grace_until`, and hard-deletes the `tenant_entitlements`
  rows (via `Unscoped()`) so the next cache miss resolves
  `DeveloperEntitlements()`. Idempotent: a row already on the Developer
  plan, or no row at all, is a no-op.
- `DowngradeToDeveloper` is a package-level function (not a method on
  `*Enforcer`) precisely so the gateway's `RequireBillingActive`
  middleware can call it without needing an `*Enforcer` instance
  plumbed through the lifecycle helpers. The webhook (`stripe` package)
  calls it the same way.
- The lifecycle middleware's `decisionPass` branch checks
  `isAutoDowngradeStatus(status) && billing.Plan != DeveloperPlan &&
  enforcer != nil`, then runs `autoDowngradeTenant` — open session,
  call `DowngradeToDeveloper`, commit, `enforcer.Invalidate` the
  cache. Every step is best-effort: a failure is logged and the
  request still proceeds (a transient DB blip should not 402 a paying
  customer).
- The Stripe webhook handlers `handleSubscriptionUpdated` (when
  `sub.Status == stripe.SubscriptionStatusCanceled`) and
  `handleSubscriptionDeleted` both call `enforcer.DowngradeToDeveloper`
  inside the same tx that persists the new status, and call
  `h.enforcer.Invalidate(...)` in their deferred commit. The
  invalidation is gated on commit success AND `billing.TenantID != 0`
  (so the early-return on lookup failure doesn't try to invalidate
  tenant 0). The in-memory `billing` struct is updated
  (`Plan = "developer"`, `GraceUntil = nil`) **before** the helper
  runs, so the subsequent `tx.Where(...).Save(&billing)` doesn't
  clobber the helper's DB writes with pre-cancelation state.

**Recovery:** the customer re-subscribes from the customer portal.
`handleCheckoutSessionCompleted` re-creates the entitlement rows and
the next request resolves `Team` entitlements. The downgrade is
fully reversible — no manual data fix-up.

## Verification

- **Unit tests**: 31 enforcer + 20 lifecycle (18 table-driven cases in `TestEvaluateBillingStatus` + 2 boundary cases in `TestEvaluateBillingStatus_GraceBoundary`) = 51/51 passing in `internal/billing/enforcer/`. The lifecycle cases suffixed `(auto-downgrade)` cover the `decisionPass` paths that the middleware translates to a one-time `DowngradeToDeveloper` + cache invalidation; the boundary test pins the `now == grace` semantics (strict-less-than — equality is treated as expired).
- **Import graph**: `cmd/gateway` continues to exclude `internal/oauthproxy`, `internal/zitadel`, `internal/portal`, `internal/admin`, `internal/signup` — the lifecycle middleware lives in `internal/billing/enforcer/` (a subpackage of `internal/billing`) so the gateway can import it without pulling the Stripe service that depends on `internal/portal/portalv1`. `servegateway` constructs its `*Enforcer` inline rather than going through `billingmount` to preserve this constraint.
- **Build / lint / race**: `go build ./...`, `go vet ./...`, `go test -race ./...`, `golangci-lint run ./...` all clean. `cmd/gateway`'s import-graph test (`import_graph_test.go`) continues to pass.
- **Integration**: Enforcement tests require a running instance; deferred to CI
- **Manual**: Invite a 2nd member on Developer plan → expect `CodePermission PermissionDenied` with `billing.limit.max-users`. Cancel a Team subscription from the customer portal → the next request on `/t/{tenant}/mcp` (or `/t/{tenant}/api/*`) should resolve Developer entitlements, the entitlement cache should be invalidated, and the `tenant_billing.plan` row should read `developer`.
