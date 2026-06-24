# Billing Plans & Entitlements

**Status**: Canonical reference — single source of truth for billing plans, features, and enforcement.
**Updated**: 2026-06-01
**Supersedes**: `docs/phases/phase-13-billing-stripe.md` (design intent — this doc is implementation reality)

## Plans

| Plan        | Price    | Human Users | Service Accounts | SA Concurrent Connections | Enforcement                                |
|-------------|----------|-------------|------------------|---------------------------|--------------------------------------------|
| **Developer** (free) | $0 | 1            | 1                | 1                         | Hard block at RPC level (4/03 Permission Denied) |
| **Team** (paid)       | $X/user/mo + $Y/SA-conn/mo | Unlimited      | Unlimited        | Unlimited                 | No enforcement; billed by reconciler       |

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
| 11 | `sso` | sso | boolean | — | enabled | `SSOSAML` | ❌ MISSING (has `sso-saml` instead) | ❌ MISSING | ❌ Not in gates | Portal SSO config | ❌ DEFERRED (TODO in `admin/settings.go`) |
| 12 | `code-mode` | code-mode | boolean | enabled | enabled | `CodeMode` | ✅ | ✅ | `CheckFeature(CodeMode)` | MCP SSE connect | ❌ DEFERRED (TODO in `transport/codemode_server.go`) |
| 13 | `custom-upstream` | custom-upstreams | boolean | enabled | enabled | `CustomUpstreams` | ✅ | ✅ | `CheckFeature(CustomUpstreams)` | Upstream creation | ❌ NOT PLANNED |
| 14 | `ide-preset` | ide-presets | boolean | enabled | enabled | `IDEPresets` | ✅ | ✅ | `CheckFeature(IDEPresets)` | IDE preset CRUD | ❌ NOT PLANNED |
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
    SSOSAML            bool   // ⚠️ MISMATCH: Go uses "sso-saml", Stripe uses "sso"
    IDEPresets         bool   // ⚠️ MISMATCH: Go uses "ide-presets", Stripe uses "ide-preset"
    CustomUpstreams    bool   // ⚠️ MISMATCH: Go uses "custom-upstreams", Stripe uses "custom-upstream"
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
| `sso` ↔ `sso-saml` naming mismatch | Webhook won't match feature name | Align naming (rename Go or Stripe) |
| `custom-upstream` ↔ `custom-upstreams` mismatch | Webhook won't match | Align naming |
| `ide-preset` ↔ `ide-presets` mismatch | Webhook won't match | Align naming |

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

**Wired (7 of 14):**
- `max-user_1` / `max-user_unlimited` → ✅ wired
- `max-service-account_1` / `max-service-account_unlimited` → ✅ wired
- `max-sa-connection_1` / `max-sa-connection_unlimited` → ✅ wired
- `code-mode` → ✅ wired
- `custom-upstream` → ✅ wired (but naming mismatch: Go expects `custom-upstreams`)
- `ide-preset` → ✅ wired (but naming mismatch: Go expects `ide-presets`)

**Not wired (7 of 14):**
- `max-upstream-link_5` / `max-upstream-link_unlimited` → ❌ deferred (v2)
- `audit-retention_7d` / `audit-retention_90d` → ❌ deferred (v2)
- `sso` → ❌ deferred (Go expects `sso-saml`)
```