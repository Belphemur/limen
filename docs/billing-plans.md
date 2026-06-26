# Billing Plans & Entitlements

**Status**: Canonical reference — single source of truth for billing plans, features, and enforcement.
**Updated**: 2026-06-26
**Supersedes**: `docs/phases/phase-13-billing-stripe.md` (design intent — this doc is implementation reality)

## Plans

| Plan        | Price    | Human Users | Service Accounts | SA Concurrent Connections | Enforcement                                |
|-------------|----------|-------------|------------------|---------------------------|--------------------------------------------|
| **Developer** (free) | $0 | 1            | 1                | 1                         | Hard block at RPC level (4/03 Permission Denied) |
| **Team** (paid)       | $X/user/mo + $Y/SA-conn/mo | Unlimited      | Unlimited        | Unlimited                 | No enforcement; billed by reconciler       |

## Plan Lifecycle

A tenant moves through a small set of billing states. The transitions are
driven by Stripe webhooks on the back end and by the lifecycle middleware
in `internal/billing/enforcer/lifecycle.go` on the request path. The two
have to agree or the UX gets confusing, so they're designed to converge
on the same `tenant_billing` row.

### Developer (free, no Stripe)

- The `tenant_billing` row is either absent or has
  `plan = "developer"` and `status = "none"`. There is no Stripe customer
  or subscription id.
- Every request resolves `DeveloperEntitlements()` from
  `entitlements.PlanEntitlements`. The hard limits
  (`MaxActiveUsers = 1`, `MaxServiceAccounts = 1`,
  `MaxSAConnections = 1`) are enforced by the per-RPC gates in
  `internal/billing/enforcer/gates.go` — exceeding them returns
  `connect.CodePermissionDenied` with a `billing.limit.*` payload.
- The lifecycle middleware is a no-op on this state
  (`status = "none"` → `decisionPass`, no downgrade candidate).

### Team (paid, normal flow)

1. **Active / trialing** — Stripe subscription is live. `status` mirrors
   Stripe verbatim. The entitlement cache is populated from the
   `tenant_entitlements` rows (populated by the
   `entitlements.active_entitlement_summary.updated` webhook). Requests
   resolve Team entitlements.
2. **`invoice.payment_failed`** — Stripe webhook sets
   `status = "past_due" | "unpaid"` and `grace_until = now + grace_days`
   (default **14**). The lifecycle middleware returns `decisionPassGrace`
   for the duration of the window: the request proceeds, the response
   carries the `X-Limen-Billing: grace` header, and the portal SPA
   renders the non-blocking warning banner.
3. **`customer.subscription.deleted` / `subscription.updated` →
   `canceled`** — the webhook runs `enforcer.DowngradeToDeveloper` in
   the same tx, setting `plan = "developer"`, clearing `grace_until`,
   hard-deleting the `tenant_entitlements` rows, and invalidating the
   entitlement cache.

### Auto-downgrade (cancellation + expired-grace recovery)

The lifecycle middleware's `decisionPass` verdict covers two Stripe
states that are *finished* rather than *paused*:

- `canceled` — subscription is gone regardless of grace
- `past_due` / `unpaid` whose grace window has expired (or was never
  set, e.g. a webhook that never fired)

For any of these, the middleware runs the **same**
`enforcer.DowngradeToDeveloper` helper the webhook uses, inside an
auto-managed tx, and invalidates the entitlement cache after commit.
The tenant's next request resolves `DeveloperEntitlements()` from
scratch and is served with developer limits rather than 402'd.

The downgrade is **one-time and idempotent** — a request that arrives
after the row is already on the Developer plan is a no-op (the helper
short-circuits on `billing.Plan == DeveloperPlan`). The billing status
remains whatever Stripe says it is (`canceled` / `past_due` /
`unpaid`); only the `plan` and `grace_until` columns and the
`tenant_entitlements` rows are mutated. The `tenant_billing` row
therefore remains an accurate mirror of Stripe truth while the
tenant operates under developer limits.

**The 402 path is now reserved for the Stripe mid-flow states**
(`incomplete`, `incomplete_expired`, `paused`) — the cases where the
customer must complete checkout or un-pause before they can come
back. These return `402 Payment Required` with
`{"error": "payment required"}` and the portal renders the
"finish checkout" page.

### Recovery

Recovery is fully reversible. The customer re-subscribes from the
customer portal:

1. `OpenCustomerPortal` RPC returns a Stripe-hosted URL.
2. The customer picks the Team plan. Stripe fires
   `checkout.session.completed` and the webhook re-creates the
   `tenant_billing` row (or updates the existing one) with
   `plan = "team"`, `status = "active"`, and a new subscription id.
3. Stripe fires
   `entitlements.active_entitlement_summary.updated` and the webhook
   re-populates the `tenant_entitlements` rows.
4. The next request invalidates / refreshes the entitlement cache
   and the tenant is back on Team entitlements.

No manual data fix-up is required at any step. The auto-downgrade
path is purely additive — the only columns it mutates are
`plan`, `grace_until`, and the `tenant_entitlements` table; the
authoritative Stripe ids (`stripe_customer_id`,
`stripe_subscription_id`) are preserved through
`handleSubscriptionDeleted` so a resubscribe reuses the same customer.

## Feature Definitions (Stripe → Go → Gate)

This table maps every entitlement from Stripe definition → Go `PlanEntitlements` field → enforcement gate. It is the **canonical cross-reference**.

| # | Stripe lookup_key | Feature name | Type | Developer limit | Team limit | Go field | Go in `EntitlementLimitFromLookupKey`? | Go in `EntitlementsFromRows`? | Gate function | Enforcement point | Status |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | `max-user_1` | max-users | numeric | 1 | — | `MaxActiveUsers` | ✅ (returns 1) | ✅ | `CheckMaxUsers` | `InviteMember` (admin RPC) | ✅ Implemented |
| 2 | `max-user_unlimited` | max-users | numeric | — | -1 (unlimited) | `MaxActiveUsers` | ✅ (returns -1) | ✅ | `CheckMaxUsers` | — (unlimited = no-op) | ✅ Implemented |
| 3 | `max-service-account_1` | max-service-accounts | numeric | 1 | — | `MaxServiceAccounts` | ✅ (returns 1) | ✅ | `CheckMaxServiceAccounts` | `CreateServiceAccount` (admin RPC) | ✅ Implemented |
| 4 | `max-service-account_unlimited` | max-service-accounts | numeric | — | -1 | `MaxServiceAccounts` | ✅ (returns -1) | ✅ | `CheckMaxServiceAccounts` | — (unlimited = no-op) | ✅ Implemented |
| 5 | `max-sa-connection_1` | max-sa-connections | numeric | 1 | — | `MaxSAConnections` | ✅ (returns 1) | ✅ | `CheckMaxSAConnections` / `CheckSAConnectionLimit` | `CallTool` (gateway — in-band MCP error) | ✅ Implemented |
| 6 | `max-sa-connection_unlimited` | max-sa-connections | numeric | — | -1 | `MaxSAConnections` | ✅ (returns -1) | ✅ | `CheckMaxSAConnections` | — (unlimited = no-op) | ✅ Implemented |
| 7 | `max-upstream-link_5` | max-upstream-links | numeric | 5 | — | ❌ Not in struct | ❌ MISSING | ❌ MISSING | ❌ Not in gates | Upstream link creation (admin) | ❌ NOT PLANNED (v1 deferral) |
| 8 | `max-upstream-link_unlimited` | max-upstream-links | numeric | — | -1 | ❌ Not in struct | ❌ MISSING | ❌ MISSING | ❌ Not in gates | — | ❌ NOT PLANNED |
| 9 | `audit-retention_7d` | audit-retention | numeric | 7 | — | ❌ Not in struct | ❌ MISSING | ❌ MISSING | ❌ Not in gates | Audit log pruning | ❌ NOT PLANNED |
| 10 | `audit-retention_90d` | audit-retention | numeric | — | 90 | ❌ Not in struct | ❌ MISSING | ❌ MISSING | ❌ Not in gates | Audit log pruning | ❌ NOT PLANNED |
| 11 | `sso` | sso | boolean | — | enabled | `SSOSAML` | ✅ | ✅ | ❌ Not in gates | Portal SSO config | ❌ DEFERRED (TODO in `admin/settings.go`) |
| 12 | `code-mode` | code-mode | boolean | enabled | enabled | `CodeMode` | ✅ | ✅ | `CheckFeature(CodeMode)` | MCP SSE connect | ❌ DEFERRED (TODO in `transport/codemode_server.go`) |
| 13 | `custom-upstream` | custom-upstream | boolean | enabled | enabled | `CustomUpstreams` | ✅ | ✅ | `CheckFeature(CustomUpstreams)` | Upstream creation | ❌ NOT PLANNED |
| 14 | `ide-preset` | ide-preset | boolean | enabled | enabled | `IDEPresets` | ✅ | ✅ | `CheckFeature(IDEPresets)` | IDE preset CRUD | ❌ NOT PLANNED |
| — | (not in Stripe) | max-projects | numeric | 5 | — | `MaxProjects` | N/A | N/A | `CheckMaxProjects` | `EnsureProject` (zitadel client) | ❌ DEFERRED |
| — | (not in Stripe) | max-storage | numeric | 1GB | 10GB | `StorageLimitMB` | N/A | N/A | `CheckStorageLimit` | File upload endpoint | ❌ DEFERRED |
| — | (not in Stripe) | advanced-ai | boolean | disabled | enabled | `AdvancedAI` | N/A | N/A | `CheckFeature(AdvancedAI)` | Tool call routing | ❌ DEFERRED |
| — | (not in Stripe) | audit-logs | boolean | disabled | enabled | `AuditLogs` | N/A | N/A | `CheckFeature(AuditLogs)` | Audit log Emit | ❌ DEFERRED |
| — | (not in Stripe) | priority-support | boolean | disabled | enabled | `PrioritySupport` | N/A | N/A | ❌ Not in gates | Support channels | ❌ NOT PLANNED |
| — | (not in Stripe) | community-support | boolean | enabled | enabled | `CommunitySupport` | N/A | N/A | ❌ Not in gates | — | ❌ NOT PLANNED |

### Current `PlanEntitlements` struct (as implemented in Go)

```go
type PlanEntitlements struct {
    MaxActiveUsers     int32  // ✅ max-user
    MaxServiceAccounts int32  // ✅ max-service-account
    MaxSAConnections   int32  // ✅ max-sa-connection
    MaxProjects        int32  // ⚠️ NOT in Stripe bootstrap — Go-only field
    CodeMode           bool   // ✅ code-mode
    AdvancedAI         bool   // ⚠️ NOT in Stripe bootstrap — Go-only field
    AuditLogs          bool   // ⚠️ NOT in Stripe bootstrap — Go-only field (Named "AuditLogs", not "audit-retention")
    SSOSAML            bool   // ✅ aligned with Stripe `sso` lookup_key
    IDEPresets         bool   // ✅ aligned with Stripe `ide-preset` lookup_key
    CustomUpstreams    bool   // ✅ aligned with Stripe `custom-upstream` lookup_key
    PrioritySupport    bool   // ⚠️ NOT in Stripe bootstrap — Go-only field
    CommunitySupport   bool   // ⚠️ NOT in Stripe bootstrap — Go-only field
    StorageLimitMB     int32  // ⚠️ NOT in Stripe bootstrap — Go-only field
}
```

## Known Gaps

### Fixed (Phase 13d fixup)

| Gap | Fix |
|-----|-----|
| `MaxServiceAccounts` missing from struct + mapping | ✅ Added field, mapping, gate; `CreateServiceAccount` now gates on `MaxServiceAccounts` |
| SA connection hard block at `StartServiceAccountConnect` | ✅ Replaced with tool-level rate-limit error in `CallTool`; removed hard block from `StartServiceAccountConnect` |

### Deferred (planned, not yet implemented)

| Gap | Impact | Fix |
|-----|--------|-----|
| `CodeMode` gate | Code-mode MCP works even when not entitled | Gate in `transport/codemode_sensor.go` |
| `AdvancedAI` gate | All users get advanced AI access regardless of plan | Gate in `gateway/manager.go:CallTool` |
| `SSOSAML` gate | SSO config visible even on Dev plan | Gate in `admin/settings.go` |
| `AuditLogs` gate | Audit events always emitted | Gate in `audit/audit.go:Emit` |
| `MaxProjects` gate | Unlimited project creation | Gate in caller of `zitadel.EnsureProject` |
| `StorageLimit` gate | No file upload endpoint exists yet | Gate when endpoint is built |

### Future (v2+)

| Gap | Impact | Fix |
|-----|--------|-----|
| `MaxUpstreamLinks` missing | Upstream link count not limited | Add field, mapping, gate |
| `AuditRetentionDays` missing | Audit retention not configurable by plan | Add field, mapping, pruning logic |
| `sso` ↔ Go `SSOSAML` field | Aligned (lookup_key `sso` maps to `SSOSAML`) | ✅ Resolved (Phase 13d fixup) |
| `custom-upstream` ↔ Go `CustomUpstreams` field | Aligned (lookup_key `custom-upstream` maps to `CustomUpstreams`) | ✅ Resolved (Phase 13d fixup) |
| `ide-preset` ↔ Go `IDEPresets` field | Aligned (lookup_key `ide-preset` maps to `IDEPresets`) | ✅ Resolved (Phase 13d fixup) |

## Enforcement Architecture

```
HTTP Request
    → tenancy.RequireTenant (resolves tenant from URL)
    → session.Interceptor (authenticates user)
    → BillingInterceptor (loads entitlements into context)
        ├─ Valkey cache hit → use cached entitlements
        └─ Cache miss → DB query → populate cache
    → Handler: ents, _ := enforcer.EntitlementsFromContext(ctx)
        ├─ Human RPC (connect.CodePermissionDenied if blocked)
        └─ MCP transport (in-band IsError:true if blocked)
```

### Error Responses

| Context | Mechanism | Example |
|---------|-----------|---------|
| Admin RPC (e.g. InviteMember) | `connect.NewError(connect.CodePermissionDenied, enforcer.CheckMaxUsers(...))` | `billing.limit.max-users: max active users reached (limit=1, usage=1)` |
| MCP tool call (e.g. SA connection over limit) | Go error from `Manager.CallTool` → codemode transport → `CallToolResult{IsError: true}` | `"SA connection limit reached (3/3). Try again later."` |

### Interceptor Chain

```
TenancyInterceptor → BearerTokenInterceptor → session.Interceptor → BillingInterceptor → session.RoleInterceptor → handler
```

The BillingInterceptor loads but does NOT enforce — enforcement happens in individual handlers because they need current usage counts (e.g., "how many active users right now?").

## Stripe Bootstrap → Go Code Mapping

The `scripts/stripe-bootstrap/main.go` defines 14 Features. Here's which are wired to Go:

**Wired (9 of 14):**
- `max-user_1` / `max-user_unlimited` → ✅ wired
- `max-service-account_1` / `max-service-account_unlimited` → ✅ wired
- `max-sa-connection_1` / `max-sa-connection_unlimited` → ✅ wired
- `code-mode` → ✅ wired
- `custom-upstream` → ✅ wired
- `ide-preset` → ✅ wired

**Not wired (5 of 14):**
- `max-upstream-link_5` / `max-upstream-link_unlimited` → ❌ deferred (v2)
- `audit-retention_7d` / `audit-retention_90d` → ❌ deferred (v2)
- `sso` → ❌ deferred (gate not yet enforced; lookup_key is aligned)
```