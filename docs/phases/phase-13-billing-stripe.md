---
phase: "13"
title: "Billing (two-plan SaaS model)"
status: planned
progress: 0
depends_on: ["4", "9b", "9c", "9i", "10", "11"]
updated: "2026-05-31"
---

# Phase 13 — Billing (two-plan SaaS model)

**Depends on**: [Phase 4](phase-04-tenant-auth-session.md) (tenant + Zitadel auth), [Phase 9b](phase-09b-portal-spa.md) (portal RPC + SPA), [Phase 9c](phase-09c-admin-spa.md) (admin SPA + RPCs), [Phase 9i](phase-09i-service-accounts.md) (service accounts — needed for SA connection billing), [Phase 10](phase-10-wiring-hardening.md) (resilience clients), [Phase 11](phase-11-production-deployment.md) (prod secrets + Caddy).
**Unblocks**: turning the gateway into a SaaS with two clear plans.

## Goal

Charge customers via [Stripe Billing](https://stripe.com/billing) using a **two-plan model**: a free **Developer** plan for a single human developer (with hard limits) and a paid **Team** plan billed per active user per month plus per concurrent service-account connection. v1 is deliberately flat-rate — no per-request metering, no overage tiers, no SLA differentiation. Active-user and SA-concurrent-connection counts come from Limen's own billing metrics pipeline (Valkey Streams → Postgres), _not_ from counting Zitadel grants.

Plan entitlements are defined via [Stripe Entitlements](https://docs.stripe.com/billing/entitlements) **Features** — boolean capabilities identified by unique `lookup_key`s, attached to Stripe Products in the Dashboard. When a customer subscribes, Stripe fires the `entitlements.active_entitlement_summary.updated` webhook. Our handler parses each `lookup_key` (e.g., `max-user_1` → feature `"max-user"`, limit `1`) and syncs the result to a local `tenant_entitlements` table. At runtime, the gating middleware reads from this table — zero Stripe API calls on the hot path.

Out of the box this phase delivers:

- A free **Developer** plan: 1 human developer (unlimited usage) + 1 service account (capped at 1 concurrent connection). Hard blocks when limits are exceeded. No Stripe subscription needed.
- A paid **Team** plan: flat per-active-user/month + per-SA-connection billing. Stripe Checkout for upgrade, Customer Portal for self-service.
- A lightweight **billing metrics pipeline** (Valkey Streams → observer consumer → Postgres) that tracks active users per month and peak concurrent SA connections, powering the reconciler.
- The portal billing page shows plan status, active counts, and a clear upgrade path.
- The staff backoffice can view tenant billing state, comp accounts, extend grace, and force-cancel — all audited.
- A public **landing SPA** (`web/landing/`) with pricing, feature comparison, privacy policy, and terms & conditions.

## Non-goals (v1)

- Per-request / per-tool-call metered billing. Designed-for, not built.
- SLA tiers or differentiated pricing per upstream.
- Custom contracts, enterprise pricing, or invoice-based payment (PO / NET-30).
- Self-serve plan switching mid-cycle (cancel and re-upgrade only).
- Usage-based / add-on pricing beyond the two Team plan prices.
- Tax remediation outside Stripe Tax. We turn it on and trust Stripe's collection rules.

## Design

### Two Plans

| Plan | Price | Human developers | Service accounts | Billing model |
|------|-------|-----------------|------------------|---------------|
| **Developer** (free) | $0 | 1 (unlimited usage) | 1 concurrent connection max | $0 Stripe subscription (tracking only) |

| **Team** (paid) | $X/user/month + $Y/SA-connection/month | Unlimited | Unlimited concurrent connections | Stripe subscription, billed monthly |

**Developer plan specifics:**

- A Stripe "Limen Developer" Product with a nominal Price (e.g. `unit_amount: 1`) that is subscribed with `quantity: 0`. This creates a $0 Stripe subscription that participates in the full subscription lifecycle — webhook pipeline, upgrade path, Customer Portal — but never generates a charge. The $1 Price + quantity=0 pattern avoids the billing-cycle-anchor reset that Stripe imposes when upgrading from a truly $0 subscription.
- The Developer Product has these Stripe Entitlements Features attached: `max-user_1`, `max-service-account_1`, `max-sa-connection_1`, `max-upstream-link_5`, `audit-retention_7d`, `code-mode`, `custom-upstream`, `ide-preset`. Each Feature's `lookup_key` encodes both the capability name and its limit using the convention `category_limit`: `max-user_1` → feature `"max-user"`, limit `1`. Boolean features (no `_` with numeric suffix) like `code-mode` mean "enabled" when present.
- Limits are resolved from the `tenant_entitlements` table at request time (see Entitlements section below) — not hard-coded constants, not config knobs.
- When either limit is exceeded a hard `402 Payment Required` is returned. No grace, no nag. The portal surfaces an "Upgrade to Team" CTA.
- On `InviteMember` when `active_users >= max-user limit`: reject. On service-account MCP connect when `concurrent_sa_connections >= max-sa-connection limit`: reject.

**Team plan specifics:**

- Billed per **active user** (any human or SA that made at least one tool call in the billing month) and per **peak concurrent SA connection** (the maximum number of simultaneous SA MCP connections observed in the billing month).
- Quantities only go _upward_ within a billing month — no mid-month downward proration. At the month boundary the reconciler resets quantity to that month's accumulating count.
- When the subscription is canceled, Stripe fires `entitlements.active_entitlement_summary.updated` with the Developer Feature set. The plan resets to Developer and limits re-apply immediately. The `stripe_customer_id` is retained for invoice history.
- Trial period configurable via `trial_days` (default 14).

### Tenant Billing Table

A new table `tenant_billing` keyed by `tenant_id` (one row per customer tenant, FK to `tenants`; staff tenant has no row):

```sql
CREATE TABLE tenant_billing (
  id                              BIGSERIAL PRIMARY KEY,
  public_id                       TEXT NOT NULL UNIQUE,            -- bil_<ulid>
  tenant_id                       BIGINT NOT NULL UNIQUE REFERENCES tenants(id),
  stripe_customer_id              TEXT,                             -- cus_...
  stripe_subscription_id          TEXT,                             -- sub_...
  status                          TEXT NOT NULL DEFAULT 'none',     -- 'none' | 'trialing' | 'active' | 'past_due' | 'unpaid' | 'canceled' | 'incomplete'
  plan                            TEXT NOT NULL DEFAULT 'developer',-- 'developer' | 'team'
  active_user_count               INTEGER NOT NULL DEFAULT 0,       -- monthly high-water mark
  active_sa_connection_count      INTEGER NOT NULL DEFAULT 0,       -- monthly high-water mark
  stripe_active_user_price_id     TEXT,                             -- price_... for per-user line
  stripe_sa_connection_price_id   TEXT,                             -- price_... for per-SA line
  current_period_end              TIMESTAMPTZ,                      -- mirrored from Stripe
  cancel_at_period_end            BOOLEAN NOT NULL DEFAULT false,
  grace_until                     TIMESTAMPTZ,                      -- past_due / unpaid soft-grace end
  last_synced_at                  TIMESTAMPTZ,                      -- reconciler watermark
  created_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at                      TIMESTAMPTZ
);
```

Note: **no** `seat_count` or `provisioned_seat_count`. Active user count (derived from the billing metrics pipeline) replaces Zitadel grant counting entirely. We do not count Zitadel grants for billing.

Stripe is the source of truth for billing state (status, period, prices, quantities); Limen mirrors the fields it needs for fast read-path gating without doing a Stripe round-trip on every request. `status` follows Stripe's [subscription status](https://stripe.com/docs/api/subscriptions/object#subscription_object-status) enum verbatim plus a synthetic `none` for "no subscription yet". `plan` is denormalized from the entitlement webhook: `'team'` when `tenant_entitlements` implies the Team plan, `'developer'` otherwise.

RLS: row is visible only when `tenant_id = current_setting('limen.tenant_id')::bigint`, plus the [Phase 12](phase-12-staff-backoffice.md) `limen.staff_mode = 'on'` clause on `SELECT`.

### Tenant Entitlements Table

Per-tenant, per-feature entitlement rows. Synced from Stripe's `entitlements.active_entitlement_summary.updated` webhook — this is the canonical source of which capabilities a tenant has access to.

```sql
CREATE TABLE tenant_entitlements (
  id           BIGSERIAL PRIMARY KEY,
  tenant_id    BIGINT NOT NULL REFERENCES tenants(id),
  feature      TEXT NOT NULL,        -- 'max-user', 'max-service-account', 'code-mode', 'sso', etc.
  limit_value  INTEGER NOT NULL,     -- -1 = unlimited / enabled, >0 = numeric cap, 0 = disabled
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, feature)
);
CREATE INDEX idx_tenant_entitlements_tenant ON tenant_entitlements (tenant_id);
```

**Lifecycle:** On `entitlements.active_entitlement_summary.updated`, the webhook handler:

1. Parses each `lookup_key` from the payload: split on last `_` → `feature` + limit.
   - `max-user_1` → feature `"max-user"`, limit `1`
   - `max-user_unlimited` → feature `"max-user"`, limit `-1`
   - `code-mode` → feature `"code-mode"`, limit `-1` (boolean features: presence = enabled)
2. In a transaction: `DELETE FROM tenant_entitlements WHERE tenant_id = ?` then `UPSERT` all current (feature, limit_value) pairs.
3. Sets `tenant_billing.plan` based on the feature set (Team if any unlimited features, Developer otherwise).

**Why a separate table** instead of a `TEXT[]` column on `tenant_billing`:

- Each entitlement is individually queryable and indexable
- Limits are stored as integers — no string parsing at request time
- Per-tenant overrides (sales exceptions, grandfathered limits) are a single INSERT
- Feature removal has a clear audit trail (DELETE row vs array mutation)

RLS: standard tenant isolation. Staff `SELECT` honours `limen.staff_mode`.

### Entitlements (Stripe Features)

Stripe's [Entitlements API](https://docs.stripe.com/billing/entitlements) uses **Features** — boolean capabilities identified by a unique `lookup_key`. Features are attached to Products in the Stripe Dashboard. When a customer subscribes to a Product, Stripe automatically creates an **Active Entitlement** for each attached Feature. When entitlements change (subscribe, upgrade, downgrade, cancel), Stripe fires the `entitlements.active_entitlement_summary.updated` webhook.

**Why Features:** They are Stripe's first-class entitlement primitive. Changing which features a plan includes requires only a Stripe Dashboard change — no code deploy, no migration. The webhook fires automatically on any change. Stripe [recommends](https://docs.stripe.com/billing/entitlements) persisting entitlements locally for fast resolution — which is exactly what `tenant_entitlements` does.

#### Feature Definitions

These Features are created in Stripe (Dashboard or API). The `lookup_key` is the contract between Stripe and Limen. Convention: `-` separates words within the category name; `_` separates the category from the limit.

| Lookup Key | Parsed Feature | Limit | Developer Product | Team Product |
|---|---|---|---|---|
| `max-user_1` | `max-user` | 1 | ✅ | — |
| `max-user_unlimited` | `max-user` | -1 | — | ✅ |
| `max-service-account_1` | `max-service-account` | 1 | ✅ | — |
| `max-service-account_unlimited` | `max-service-account` | -1 | — | ✅ |
| `max-sa-connection_1` | `max-sa-connection` | 1 | ✅ | — |
| `max-sa-connection_unlimited` | `max-sa-connection` | -1 | — | ✅ |
| `max-upstream-link_5` | `max-upstream-link` | 5 | ✅ | — |
| `max-upstream-link_unlimited` | `max-upstream-link` | -1 | — | ✅ |
| `audit-retention_7d` | `audit-retention` | 7 | ✅ | — |
| `audit-retention_90d` | `audit-retention` | 90 | — | ✅ |
| `sso` | `sso` | -1 | — | ✅ |
| `code-mode` | `code-mode` | -1 | ✅ | ✅ |
| `custom-upstream` | `custom-upstream` | -1 | ✅ | ✅ |
| `ide-preset` | `ide-preset` | -1 | ✅ | ✅ |

Boolean features (no `_` + numeric suffix) mean "enabled when present" and are stored with `limit_value = -1`.

**Plan derivation:** The `tenant_billing.plan` column is set by the webhook handler. If any feature has `limit_value = -1` (i.e., the feature set includes unlimited capabilities), `plan = 'team'`. Otherwise `plan = 'developer'`. The `plan` column is a convenience denormalization — `tenant_entitlements` is the canonical source.

#### Webhook Payload

The `entitlements.active_entitlement_summary.updated` webhook delivers the customer's full active entitlement list:

```json
{
  "type": "entitlements.active_entitlement_summary.updated",
  "data": {
    "object": {
      "customer": "cus_xxx",
      "entitlements": {
        "data": [
          {"lookup_key": "max-user_1"},
          {"lookup_key": "code-mode"},
          {"lookup_key": "ide-preset"}
        ],
        "has_more": false
      }
    },
    "previous_attributes": {
      "entitlements": {"data": []}
    }
  }
}
```

The handler operates on the **full list** — it does not diff against `previous_attributes`. Every handler invocation is a complete overwrite (DELETE + UPSERT), making it idempotent and resilient to missed deliveries. The `has_more` flag indicates pagination (the summary payload caps at 10 entitlements; our two plans need at most 9, so pagination is unlikely but handled if needed).

#### Entitlement Resolution (Request Time)

At request time, the gating middleware reads all rows for the tenant and maps them to a struct:

```go
type PlanEntitlements struct {
    MaxHumanUsers              int // -1 = unlimited, >0 = numeric cap, 0 = disabled
    MaxServiceAccounts         int
    MaxConcurrentSAConnections int
    MaxUpstreamLinks           int
    AuditLogRetentionDays      int
    SSO                        bool
    CodeMode                   bool
    CustomUpstreams            bool
    IDEPresets                 bool
}

func EntitlementsFromRows(rows []TenantEntitlement) PlanEntitlements {
    e := PlanEntitlements{} // all zero = everything disabled
    for _, r := range rows {
        switch r.Feature {
        case "max-user":
            e.MaxHumanUsers = r.LimitValue
        case "max-service-account":
            e.MaxServiceAccounts = r.LimitValue
        case "max-sa-connection":
            e.MaxConcurrentSAConnections = r.LimitValue
        case "max-upstream-link":
            e.MaxUpstreamLinks = r.LimitValue
        case "audit-retention":
            e.AuditLogRetentionDays = r.LimitValue
        case "sso":
            e.SSO = true
        case "code-mode":
            e.CodeMode = true
        case "custom-upstream":
            e.CustomUpstreams = true
        case "ide-preset":
            e.IDEPresets = true
        }
    }
    return e
}
```

The `lookup_key` → `feature` split is done once in the webhook handler. The request-time resolver does zero string parsing — just an O(1) switch on pre-parsed feature names. The struct can be cached for the request duration (e.g., stored on `context.Context` after the first read).

### Stripe Products & Prices

Two Stripe products:

1. **"Limen Developer"** — free tracking product with a nominal Price (used with `quantity: 0` to create a free subscription). Features attached: `max-user_1`, `max-service-account_1`, `max-sa-connection_1`, `max-upstream-link_5`, `audit-retention_7d`, `code-mode`, `custom-upstream`, `ide-preset`. No payment is ever collected.
2. **"Limen Team"** — paid product with two prices:
   - **Per-active-user price** (`active_user_price_id`): flat rate, integer quantity = number of distinct active users in the billing month.
   - **Per-SA-connection price** (`sa_connection_price_id`): flat rate, integer quantity = peak concurrent SA connections observed in the billing month.

Features attached to Team: `max-user_unlimited`, `max-service-account_unlimited`, `max-sa-connection_unlimited`, `max-upstream-link_unlimited`, `audit-retention_90d`, `sso`, `code-mode`, `custom-upstream`, `ide-preset`.

Both prices are quantity-based (not metered usage). Stripe handles proration on mid-cycle quantity increases automatically.

**Dashboard setup:**
1. Create 14 Features (Catalog → Features) with the lookup_keys from the Feature Definitions table above
2. Attach Features to the Developer Product (8 features) and Team Product (9 features)
3. Features and their attachments are managed exclusively in the Stripe Dashboard — not in config.yaml, not in migrations. Changing entitlements is a business operation, not a code change.

### Reconciliation Rules

- Quantities go **only upward** within a billing month. If active users rise from 3 to 7 mid-month, Stripe is updated to 7 immediately. If users drop back to 4, Stripe stays at 7 until the next billing period.
- At the **month boundary**, the reconciler first runs and resets the Stripe quantity to the count accumulating in the new month's `active_user_months` / `sa_connection_snapshots` rows.
- Reconciler is triggered by:
  1. **Periodic** every 1 h (jittered) over `tenant_billing` rows with `status IN ('trialing','active','past_due')`.
  2. **Reactive** on "new active user this month" or "new peak SA connection" event from the billing metrics pipeline. The pipeline emits the event to Valkey; a lightweight listener fires the reconciler for that tenant.
- Stripe outages do not block the pipeline — the periodic loop catches up. Wrapped in `internal/resilience.Client("stripe.subscription_items", cfg)` ([Phase 10](phase-10-wiring-hardening.md)).

### Subscribe / Upgrade Flow

```
SPA: "Upgrade to Team" CTA on /t/<tenant>/portal/billing
  → SPA calls portal RPC CreateCheckoutSession()
  → Limen: ensure stripe_customer_id (create Customer if absent, name = tenant.name, metadata.tenant_public_id = <ulid>)
  → Limen: read current active_user_count and active_sa_connection_count for this month
  → Limen: stripe.CheckoutSession.Create({
        mode='subscription',
        customer=<cus>,
        line_items=[
          {price=<config.team_active_user_price_id>, quantity=<active_user_count>, adjustable_quantity: true},
          {price=<config.team_sa_connection_price_id>, quantity=<active_sa_connection_count>, adjustable_quantity: true},
        ],
        success_url='/t/<tenant>/portal/billing?status=ok',
        cancel_url='/t/<tenant>/portal/billing?status=cancel',
        automatic_tax={enabled: true},
        client_reference_id='<tenant_id>',
        subscription_data={ trial_period_days: <config.trial_days> },
      })
  → Limen returns session.url
  → SPA window.location = session.url
```

On `checkout.session.completed` the webhook handler:

- Loads `tenant_billing` via `client_reference_id`.
- Persists `stripe_subscription_id`, both price IDs, `plan = 'team'`, `status` (`trialing` or `active`), `current_period_end`.
- Runs `Reconcile` once to lock in both quantities.
- The `entitlements.active_entitlement_summary.updated` webhook will fire separately (shortly after) and sync the `tenant_entitlements` table. Until then, the tenant remains on Developer entitlements — acceptable for the brief window between checkout completion and the entitlement webhook arrival.

Initial quantity is typically 1 (the subscribing user) and 0–1 SA connections.

### Cancel / Downgrade

- Customer clicks **Cancel subscription** in Stripe Customer Portal.
- Stripe fires `customer.subscription.deleted` webhook.
- Stripe also fires `entitlements.active_entitlement_summary.updated` with the Developer Feature set.
- Webhook handler (subscription): sets `status = 'canceled'`, `plan = 'developer'`. The `stripe_customer_id` is retained for invoice history.
- Webhook handler (entitlement): DELETE + UPSERT `tenant_entitlements` to the Developer feature set. The entitlement handler always overwrites — ordering between subscription and entitlement webhooks doesn't matter; the last one to land produces a consistent state.
- Developer plan limits re-apply immediately on the next request.
- If the customer resubscribes later, `plan` flips back to `'team'`, `stripe_subscription_id` is repopulated, and entitlements are re-synced.

### Webhook Endpoint

Mounted at `/billing/stripe/webhook` (root-level, no tenant prefix — Stripe doesn't know about tenants). Routing per event:

| Event | Action |
|-------|--------|
| `checkout.session.completed` | Persist subscription ID + both price IDs; flip `plan='team'` and `status`; `Reconcile` once. |
| `customer.subscription.updated` | Mirror `status`, `current_period_end`, `cancel_at_period_end`, price IDs. |
| `customer.subscription.deleted` | Set `status='canceled'`, `plan='developer'`; clear `stripe_subscription_id`; retain `stripe_customer_id` for invoice history. |
| `invoice.payment_failed` | Set `grace_until = now + config.grace_days` (default 7); status mirrors (`past_due` / `unpaid`). |
| `invoice.payment_succeeded` | Clear `grace_until`. |
| `entitlements.active_entitlement_summary.updated` | Parse lookup_keys → DELETE + UPSERT `tenant_entitlements` for the tenant. Derive `plan` column from feature set. Handle `has_more` pagination if present. Idempotent (complete overwrite). |

Both subscription and entitlement webhook types share the same endpoint path and the same signature verification. The handler dispatches by event `type`.

Signature verified with `stripe.Webhook.ConstructEvent`. Replay-safe: every handler is idempotent — we look up by Stripe object ID and either insert or update. Webhook secret comes from `config.yaml` (`billing.stripe.webhook_secret`), wired through `${STRIPE_WEBHOOK_SECRET}` env-substitution.

The webhook handler is **not** behind a tenant or session middleware — but it is behind:

- HTTPS + the Caddy proxy (which strips inbound `X-Forwarded-*` Stripe doesn't set).
- The resilience-wrapped HTTP server with a request-size cap (Stripe events are < 100 KB).
- A 5-second handler timeout; the handler enqueues to an in-memory channel and ACKs immediately. The drain goroutine is what calls Stripe back / mutates Postgres. This keeps Stripe's 30 s ack deadline comfortable even when Postgres is slow.

### Gating Middleware

A new middleware `RequireBillingActive` lives in `internal/billing/enforcer/lifecycle.go` (gateway forbids importing `internal/billing` directly — see `cmd/gateway/import_graph_test.go`; the `enforcer` subpackage is the safe surface):

```
read tenant_billing + tenant_entitlements (single query with JOIN)
    → resolve PlanEntitlements from tenant_entitlements rows
    → billing.enabled == false                                              → pass (short-circuit)
    → tenant is _staff                                                      → pass (exempt)

    → plan == 'developer'
        → check current usage against resolved PlanEntitlements limits     → pass if within limits
        → otherwise                                                        → 402 Payment Required (hard block)
    → plan == 'team' AND status ∈ {'trialing','active'}                    → pass
    → plan == 'team' AND status ∈ {'past_due','unpaid'}
        ∧ now < grace_until                                                → pass with warning header (X-Limen-Billing: grace)
    → plan == 'team' AND status ∈ {'past_due','unpaid'}
        ∧ now ≥ grace_until                                                → 402 Payment Required
    → plan == 'team' AND status == 'canceled'                              → 402 Payment Required
```

Mounted on:

- `/t/{tenant}/mcp` (the value-generating path) — gated.
- `/t/{tenant}/api/*` (portal RPCs) — gated **except** the billing sub-namespace (`limen.portal.v1.BillingService/*`). The transport layer checks `chi.RouteContext().RoutePath` and bypasses the gate when the matched procedure falls under that prefix, so a tenant whose subscription has expired can still call `GetBillingSummary` (to render the "expired" page) and `OpenCustomerPortal` (to pay and recover). An admin always needs to be able to click "Pay now."

The middleware reads `tenant_entitlements` (not hard-coded constants) for plan enforcement. The `plan` column is used only for billing state gating (trialing/active/past_due). Entitlement limits like `max-user` are enforced by comparing current counts against `PlanEntitlements` resolved from the DB:

- `InviteMember` RPC: when `MaxHumanUsers > 0` and `active_users >= MaxHumanUsers`, return `402` with structured error `billing.limit.max_users`.
- SA connection (MCP transport init): when `MaxConcurrentSAConnections > 0` and `concurrent_sa_connections >= MaxConcurrentSAConnections`, return `402` with MCP error `billing.limit.max_sa_connections`.

These are hard blocks — no grace period, no nag.

### Config

```yaml
billing:
  enabled: true
  stripe:
    api_key: "${STRIPE_API_KEY}"
    webhook_secret: "${STRIPE_WEBHOOK_SECRET}"
    publishable_key: "${STRIPE_PUBLISHABLE_KEY}"
  products:
    developer_price_id: "price_..."          # tracking-only, $0
    team_active_user_price_id: "price_..."
    team_sa_connection_price_id: "price_..."
  trial_days: 14
  grace_days: 7
```

Note: Developer plan limits are **not** in config — they are defined by Stripe Entitlements Features attached to the Developer Product, synced to `tenant_entitlements`, and enforced from there. Feature lookup_keys and Product-Feature attachments are managed in the Stripe Dashboard, not in config.yaml or migrations. If `billing.enabled: false` (self-hosters who don't want Stripe), the gating middleware short-circuits to pass-through and the portal billing page is hidden.

## Sub-phases

### 13a: Landing SPA + Legal Pages

Goal: Scaffold `web/landing/` as a separate Vite + Vue 3 SPA in the pnpm workspace. Served at the root path (public, no auth). Includes pricing page comparing Developer vs Team plans, privacy policy, and terms & conditions. Not part of the backend billing work — just sub-phase planning.

- [ ] `web/landing/` scaffolded with Vite + Vue 3 + TypeScript under pnpm workspace
- [ ] `web/landing/package.json` added to `pnpm-workspace.yaml`
- [ ] Landing page with hero, feature grid, pricing cards (Developer vs Team), CTA to sign up
- [ ] `/privacy` route with privacy policy content
- [ ] `/terms` route with terms & conditions content
- [ ] `pnpm build` produces `web/landing/dist/`
- [ ] Caddy/nginx config updated to serve landing SPA at root `/`, with API routes proxied

### 13b: Billing Metrics Pipeline (Valkey Streams)

See [Phase 13b — Billing Metrics Pipeline](phase-13b-billing-metrics-pipeline.md) for the full design, tables, recorder, consumer, and checklist. This sub-phase delivers the `active_user_months` and `sa_connection_snapshots` tables that the Stripe reconciler (13c) reads from.

### 13c: Stripe Integration + Bootstrap

See [Phase 13c — Stripe Integration](phase-13c-stripe-integration.md) for the full design, including the Stripe bootstrap script (`scripts/stripe-bootstrap/`), database models, webhook handler, BillingService RPCs, reconciler, and checklist.

### 13d: Plan Enforcement

Goal: Middleware and guards that enforce plan limits. Hard blocks for Developer plan exceeds. Grace-period soft blocks for Team plan payment failures.

- [x] `internal/billing/enforcer/lifecycle.go` — `RequireBillingActive` middleware
- [ ] Developer plan enforcement: `InviteMember` rejects if already 1 active user
- [ ] Developer plan enforcement: SA connection rejects if already 1 concurrent SA connection
- [ ] Team plan gating: status check (trialing/active → pass, past_due+grace → pass+warn, past_due+nograce → 402, canceled → 402)
- [ ] Middleware mounted on `/t/{tenant}/mcp` and `/t/{tenant}/api/*` except billing namespace
- [ ] Staff tenant exempt from all billing gates
- [ ] `billing.enabled: false` short-circuits middleware to pass-through
- [ ] Structured error responses for each gate failure (JSON for RPC, MCP error for MCP path)

### 13e: Portal Billing Page + Upgrade Flow

Goal: Tenant owner sees plan status, active counts, and can upgrade from Developer to Team. Billing page in the portal SPA.

- [ ] `web/portal/src/pages/Billing.vue` — plan status display (Developer or Team), active user count, SA connection count
- [ ] Developer plan view: shows "You're on the Developer plan" with limits shown, "Upgrade to Team" CTA
- [ ] Team trialing view: badge "Trial — N days left", "Manage subscription" button
- [ ] Team active view: current period, next bill date, "Manage subscription" button
- [ ] Team past_due view: red banner, "Update payment method" CTA
- [ ] Team canceled view: "Your Team plan has ended" with "Resubscribe" button, note about Developer plan limits
- [ ] Nav item for billing page, gated on `role=owner`
- [ ] Global past-due banner component for Team plan tenants
- [ ] SPA receives Stripe publishable key via `GetBillingSummary` response

### 13f: Staff Backoffice Billing Extensions

Goal: Staff (`super_admin`) can view tenant billing state, comp accounts, extend grace periods, force cancel.

- [ ] `proto/limen/staff/v1/staff.proto` extended: `GetTenantBilling`, `ExtendGrace`, `CompTenant`, `ForceCancel`
- [ ] `GetTenantBilling` RPC: full mirror of `tenant_billing` + recent invoices from Stripe API
- [ ] `ExtendGrace(tenant_id, until)` RPC: override `grace_until`, audited
- [ ] `CompTenant(tenant_id, until)` RPC: set `status=active` regardless of Stripe state, audited
- [ ] `ForceCancel(tenant_id, reason)` RPC: cancel Stripe sub immediately, audited
- [ ] Staff SPA: Tenants detail card shows billing block (plan, active counts, MRR, payment method, last invoice)
- [ ] `staff_audit_log` records: `billing.comp`, `billing.extend_grace`, `billing.force_cancel`

## Deliverables

| File | Change |
|------|--------|
| `docs/phases/phase-13-billing-stripe.md` | This file — complete rewrite |
| `docs/phases/README.md` | Updated index |
| `internal/billing/` | New package (client, metrics, stripe, middleware, service) |
| `internal/billing/entitlements.go` | `PlanEntitlements` struct + `EntitlementsFromRows()` resolver |
| `internal/billing/metrics/` | Billing metrics recorder + consumer |
| `internal/billing/stripe/` | Stripe SDK wrapper, webhook handler, service |
| `proto/limen/portal/v1/portal.proto` | Add `BillingService` |
| `proto/limen/staff/v1/staff.proto` | Add billing RPCs |
| `config.yaml` | New `billing:` section |
| `web/landing/` | New landing SPA |
| `web/portal/src/pages/Billing.vue` | New billing page |
| `cmd/observer/` or `cmd/limen/` | Billing consumer goroutine |
| migrations/ | `tenant_billing`, `tenant_entitlements`, `active_user_months`, `sa_connection_snapshots` |

## Verification

- **Subscribe happy path**: tenant owner clicks "Upgrade to Team" → Stripe Checkout opens in test mode → completes with test card `4242 4242 4242 4242` → returns to portal with `status=trialing`, `plan=team` → `active_user_count` matches billing metrics (likely 1).
- **Entitlement webhook — subscribe**: subscribe to Developer via Stripe → `entitlements.active_entitlement_summary.updated` fires with Developer lookups → `tenant_entitlements` populated with 8 rows → portal shows Developer plan → MCP enforces 1-user limit.
- **Entitlement webhook — upgrade**: upgrade from Developer to Team → entitlement webhook fires with Team lookups → `tenant_entitlements` updated (Developer rows deleted, Team rows inserted) → `plan` flips to `'team'` → limits lifted.
- **Entitlement webhook — cancel**: cancel Team subscription → entitlement webhook fires with Developer lookups → `tenant_entitlements` reset → `plan` flips to `'developer'` → 1-user limit re-applies.
- **Startup reconciliation**: simulate a missed entitlement webhook (gateway was down) → restart gateway → startup reconciliation calls Stripe API → `tenant_entitlements` repaired.
- **Developer plan hard limits**: Developer tenant attempts to invite a 2nd user → `InviteMember` returns 402. Developer tenant's service account attempts a 2nd concurrent MCP connection → 402.
- **Payment failure → grace**: trigger `invoice.payment_failed` from Stripe CLI → `grace_until` set 7 days out → portal banner shows → MCP still works during grace → after grace expires (or force `now > grace_until`) → MCP returns 402.
- **Payment recovery**: trigger `invoice.payment_succeeded` after a `payment_failed` → grace cleared → banner gone.
- **Cancel → reset**: tenant cancels via Customer Portal → `customer.subscription.deleted` fires → `plan` resets to `developer` → Developer hard limits re-apply immediately.
- **Downgrade mid-month**: quantities don't decrease mid-cycle; next month's reconciler resets to the new month's accumulating count.
- **Staff comp tenant**: `super_admin` calls `CompTenant(tenant_id, +30d)` → tenant routes pass even with no Stripe sub → audit row written.
- **Staff extend grace**: `super_admin` calls `ExtendGrace(tenant_id, +14d)` → `grace_until` extended → middleware passes during extended grace → audit row written.
- **`billing.enabled: false`**: portal nav has no Billing item; gating middleware is a pass-through; staff backoffice billing card shows "billing disabled."
- **Staff tenant exempt**: log in to `/t/_staff/portal/` with no Stripe configured → backoffice loads cleanly, no billing gates fire.

## Risks

- **Stripe outage during quantity updates**: mitigated by the periodic (1 h) reconciler — count drift heals on its own. Verified in tests by simulating a 503 from Stripe and confirming the next reconcile run repairs the count.
- **Webhook ordering**: Stripe can deliver webhooks out of order. Mitigation: handlers are state-mirroring (not state-mutating-by-delta), so a late `subscription.updated` containing an older `current_period_end` is detected via `created` timestamp comparison and dropped.
- **Developer plan limits may frustrate legitimate use**: documented. The upgrade path is one click from the portal billing page.
- **Proration on mid-cycle additions**: Stripe handles automatically for quantity increases, surfaced on the invoice as a prorated line item.
- **Entitlement webhook ordering**: The `entitlements.active_entitlement_summary.updated` webhook can arrive slightly after `customer.subscription.created` (independent events). Mitigation: the entitlement handler is a complete overwrite (DELETE + UPSERT), so ordering doesn't matter — the last event to land produces consistent state. Startup reconciliation calls Stripe's List Active Entitlements API for every active tenant to repair any gaps.
- **Feature lookup_key changes**: If a lookup_key is renamed in Stripe without a corresponding `case` in `EntitlementsFromRows()`, the feature is silently ignored. This is intentional — the resolver is the gatekeeper. Keep the Feature Definitions table up to date.

## Checklist

- [ ] `tenant_billing` migration with RLS policies + partial unique on `(tenant_id) WHERE deleted_at IS NULL` + the staff-mode SELECT clause from Phase 12
- [ ] `tenant_entitlements` migration: table with `(tenant_id, feature)` unique constraint + RLS + index on `tenant_id`
- [ ] Stripe Dashboard: 14 Features created with correct lookup_keys
- [ ] Stripe Dashboard: Features attached to Developer Product (8) and Team Product (9)
- [ ] `internal/billing/entitlements.go` — `PlanEntitlements` struct + `EntitlementsFromRows()` resolver
- [ ] `entitlements.active_entitlement_summary.updated` webhook handler: parse lookup_keys → DELETE + UPSERT `tenant_entitlements` → derive `plan`
- [ ] Startup entitlement reconciliation: List Active Entitlements API for active tenants
- [ ] Gating middleware: reads `tenant_entitlements` (not hard-coded limits) for enforcement
- [ ] `active_user_months` migration with RLS + indexes on `(tenant_id, month_start)`
- [ ] `sa_connection_snapshots` migration with RLS + indexes on `(tenant_id, service_account_id, connected_at)`
- [ ] `internal/billing/` package: client, metrics, stripe, middleware, service
- [ ] `internal/billing/metrics/recorder.go` — `BillingRecorder` for Valkey Streams
- [ ] Stripe SDK (`github.com/stripe/stripe-go/v82`) added to `go.mod`; SDK calls go through `internal/resilience.Client("stripe.<endpoint>", cfg)`
- [ ] `proto/limen/portal/v1/portal.proto` extended with `BillingService` (owner-only)
- [ ] `proto/limen/staff/v1/staff.proto` extended with `GetTenantBilling`, `ExtendGrace`, `CompTenant`, `ForceCancel` (all audited)
- [x] `RequireBillingActive` middleware mounted on `/t/{tenant}/mcp` and `/t/{tenant}/api/*` except the billing namespace; staff tenant exempt
- [ ] Developer plan hard enforcement in `InviteMember` and SA connect paths
- [ ] Webhook handler at `/billing/stripe/webhook` with Stripe signature verification, idempotency by Stripe object ID, async drain
- [ ] Billing metrics pipeline: Valkey Streams `billing:active_users` + `billing:sa_connections` → observer consumer → `COPY` → Postgres
- [ ] Reconciler: 1 h jittered periodic loop + reactive hook on new active-user / SA-connection event
- [ ] `web/landing/` SPA scaffold + pricing page + `/privacy` + `/terms`
- [ ] SPA `Billing.vue` page + global past-due banner + nav item gated on `role=owner`
- [ ] Developer plan portal view with "Upgrade to Team" CTA
- [ ] Team plan portal views (trialing / active / past_due / canceled)
- [ ] Staff SPA: billing block on tenant detail card + `staff_audit_log` entries
- [ ] `config.yaml` `billing:` section with `enabled`, Stripe key/secret refs, two product price IDs, `trial_days`, `grace_days`
- [ ] Stripe Dashboard runbook: two products (Developer + Team), two prices, 14 Features attached to products, webhook endpoint, Customer Portal toggles, Tax configuration
- [ ] Integration tests using Stripe test mode + Stripe CLI for webhook replay covering every Verification scenario
- [ ] `billing.enabled: false` short-circuits the middleware and hides the portal nav item — self-host path stays free
