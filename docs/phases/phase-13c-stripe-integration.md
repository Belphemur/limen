---
phase: "13c"
title: "Stripe Integration"
status: in_progress
progress: 37
depends_on: ["4", "9b", "9c", "9i", "10", "11", "13b"]
updated: "2026-05-31"
---

# Phase 13c — Stripe Integration

**Depends on**: Phase 4 (tenant auth), Phase 9b (portal SPA), Phase 9c (admin SPA), Phase 9i (service accounts), Phase 10 (resilience), Phase 11 (prod deploy), Phase 13b (billing metrics pipeline).
**Unblocks**: Phase 13d (plan enforcement), Phase 13e (portal billing page), Phase 13f (staff backoffice billing).

## Goal

Integrate Stripe Billing SDK into Limen: two products (Developer + Team), checkout session creation, customer portal, webhook handler with signature verification and idempotency, entitlement resolution from Stripe Features, and a reconciler that keeps Stripe quantities in sync with the billing metrics pipeline. Plus a standalone bootstrap script that declaratively manages Stripe state (Products, Prices, Features, Product Feature attachments, Webhook endpoints) as infrastructure-as-code in the repo.

## Design

This phase has two major components:

### 13c-a: Stripe Bootstrap Script (`scripts/stripe-bootstrap/`)

A standalone Go module following the same pattern as `scripts/zitadel-bootstrap/`:

- **Separate module** — `go.mod` only depends on `stripe-go/v82`, not the Limen codebase
- **Declarative state** — define desired Products, Prices, Features, Product Feature attachments, and Webhook endpoints in the Go source
- **`ensureX` pattern** — every resource helper searches for existing resource first, creates if missing, updates if the definition changed, and is safe to re-run
- **Archive/delete** — Features and Products cannot be deleted via the Stripe API. Instead, the bootstrap marks resources not in the desired state as `active: false` (archive) and logs them. Resources that exist but are not in the desired set are archived — the bootstrap converges to the declared state.
- **Outputs IDs** — writes `STRIPE_DEVELOPER_PRODUCT_ID`, `STRIPE_TEAM_PRODUCT_ID`, `STRIPE_TEAM_ACTIVE_USER_PRICE_ID`, `STRIPE_TEAM_SA_CONNECTION_PRICE_ID`, `STRIPE_WEBHOOK_SECRET`, etc. to `.bootstrap-out.env`
- **Idempotent** — safe to re-run, only touches resources that differ from desired state

**Resource definitions (declared in Go source):**

```go
type DesiredState struct {
    Products []DesiredProduct
    Prices   []DesiredPrice
    Features []DesiredFeature
    Attachments []DesiredProductFeature  // which features go on which product
    Webhooks []DesiredWebhookEndpoint
}

type DesiredProduct struct {
    Key         string // "developer" or "team"
    Name        string // "Limen Developer" or "Limen Team"
    Description string
    Active      bool
    Metadata    map[string]string
}

type DesiredPrice struct {
    Key        string // "dev_tracking", "team_active_user", "team_sa_connection"
    ProductKey string // references DesiredProduct.Key
    Currency   string
    UnitAmount int64  // in cents, 0 for tracking
    Interval   string // "month"
    UsageType  string // "licensed"
    LookupKey  string // unique identifier for config.yaml
}

type DesiredFeature struct {
    LookupKey string // e.g. "max-user_1", "code-mode"
    Name      string // "Maximum 1 Active User"
    Active    bool
}

type DesiredProductFeature struct {
    ProductKey string // references DesiredProduct.Key
    FeatureKey string // references DesiredFeature.LookupKey
}

type DesiredWebhookEndpoint struct {
    URL           string
    EnabledEvents []string
    Description   string
}
```

**Convergence logic:**

1. List all existing Products from Stripe → for each, compare with desired state:
   - In desired & matches → no-op
   - In desired & differs → update (name, description, metadata)
   - Not in desired → set `active: false` (archive), log warning
   - In desired but not existing → create
2. List all existing Prices → same converge pattern (by lookup_key)
3. List all existing Features → converge (by lookup_key); note: Features cannot be deleted, only archived
4. For each Product, list attached Features → converge attachments (attach missing, no-op matching)
5. List webhook endpoints → converge (by URL)

**Env var inputs**: `STRIPE_API_KEY` (required), `STRIPE_WEBHOOK_URL` (optional — skips webhook creation if not set; useful for dev/CI where no public endpoint is available)

**Run**: `go run ./scripts/stripe-bootstrap/` or integrated into `make stripe-bootstrap`

### 13c-b: Stripe Backend Integration

Stripe SDK integration into the Limen gateway following the phase-13 spec design:

#### Database Tables

`tenant_billing` — one row per customer tenant, mirrors Stripe subscription state:

```sql
CREATE TABLE tenant_billing (
  id                         BIGSERIAL PRIMARY KEY,
  public_id                  TEXT NOT NULL UNIQUE,
  tenant_id                  BIGINT NOT NULL UNIQUE REFERENCES tenants(id),
  stripe_customer_id         TEXT,
  stripe_subscription_id     TEXT,
  status                     TEXT NOT NULL DEFAULT 'none',
  plan                       TEXT NOT NULL DEFAULT 'developer',
  active_user_count          INTEGER NOT NULL DEFAULT 0,
  active_sa_connection_count INTEGER NOT NULL DEFAULT 0,
  stripe_active_user_price_id    TEXT,
  stripe_sa_connection_price_id  TEXT,
  current_period_end         TIMESTAMPTZ,
  cancel_at_period_end       BOOLEAN NOT NULL DEFAULT false,
  grace_until                TIMESTAMPTZ,
  last_synced_at             TIMESTAMPTZ,
  created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at                 TIMESTAMPTZ
);
```

`tenant_entitlements` — per-tenant feature entitlements synced from Stripe webhook:

```sql
CREATE TABLE tenant_entitlements (
  id          BIGSERIAL PRIMARY KEY,
  tenant_id   BIGINT NOT NULL REFERENCES tenants(id),
  feature     TEXT NOT NULL,
  limit_value INTEGER NOT NULL,   -- -1 = unlimited/enabled, >0 = numeric cap, 0 = disabled
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, feature)
);
CREATE INDEX idx_tenant_entitlements_tenant ON tenant_entitlements (tenant_id);
```

#### Config

```yaml
billing:
  enabled: true
  stripe:
    api_key: "${STRIPE_API_KEY}"
    webhook_secret: "${STRIPE_WEBHOOK_SECRET}"
    publishable_key: "${STRIPE_PUBLISHABLE_KEY}"
  products:
    developer_price_id: "price_..."
    team_active_user_price_id: "price_..."
    team_sa_connection_price_id: "price_..."
  trial_days: 14
  grace_days: 7
```

#### Stripe Client Wrapper

`internal/billing/stripe/client.go`:
- Wrapper around `github.com/stripe/stripe-go/v82`
- Uses `internal/resilience.Client("stripe.<endpoint>", cfg)` for all HTTP calls
- Customer lookup/create by tenant ID with `tenant_public_id` in metadata
- Checkout session creation with line items for both prices
- Customer Portal session creation

#### Webhook Handler

`internal/billing/stripe/webhook.go` — mounted at `/billing/stripe/webhook` (root-level, no tenant prefix):
- Signature verification via `stripe.Webhook.ConstructEvent`
- Async drain: enqueue to in-memory channel, ACK immediately, drain goroutine mutates Postgres
- 5-second handler timeout; 30s Stripe ack deadline

Webhook event handling (idempotent by Stripe object ID):

| Event | Action |
|-------|--------|
| `checkout.session.completed` | Persist sub ID + both price IDs; flip `plan='team'` and `status`; run Reconcile once |
| `customer.subscription.updated` | Mirror `status`, `current_period_end`, `cancel_at_period_end`, price IDs |
| `customer.subscription.deleted` | Set `status='canceled'`, `plan='developer'`; clear `stripe_subscription_id`; retain `stripe_customer_id` |
| `invoice.payment_failed` | Set `grace_until = now + config.grace_days`; status mirrors Stripe (`past_due` / `unpaid`) |
| `invoice.payment_succeeded` | Clear `grace_until` |
| `entitlements.active_entitlement_summary.updated` | Parse lookup_keys → DELETE + UPSERT `tenant_entitlements` for the tenant → derive `plan` column from feature set |

#### Billing Service RPCs

`internal/billing/stripe/service.go` — `BillingService` Connect-RPC handlers (owner-only):

- `GetBillingSummary` — returns plan, status, active counts, current period, Stripe publishable key
- `CreateCheckoutSession` — creates Stripe Checkout for Team upgrade with both line items, initial quantities from billing metrics
- `OpenCustomerPortal` — creates Stripe Customer Portal session for self-service

Proto added to `proto/limen/portal/v1/portal.proto`.

#### Entitlements Resolver

`internal/billing/entitlements.go`:
- `PlanEntitlements` struct (MaxHumanUsers, MaxServiceAccounts, MaxConcurrentSAConnections, MaxUpstreamLinks, AuditLogRetentionDays, SSO, CodeMode, CustomUpstreams, IDEPresets)
- `EntitlementsFromRows()` — resolves from `tenant_entitlements` rows with O(1) switch, zero string parsing
- Lookup_key → feature split done once in webhook handler; resolver just reads pre-parsed feature names

#### Reconciler

- **Periodic**: 1h jittered loop over `tenant_billing` rows with `status IN ('trialing','active','past_due')`
- **Reactive**: hook on new active-user / SA-connection event from billing metrics pipeline
- Quantities go only upward within a billing month
- Month boundary: reconciler resets to new month's accumulating count
- Stripe quantity updates wrapped in resilience client

#### Startup Reconciliation

- Call Stripe List Active Entitlements API for each tenant with active subscription on gateway startup
- Repairs any missed webhook deliveries during gateway downtime

## Deliverables

| File | Change |
|------|--------|
| `docs/phases/phase-13c-stripe-integration.md` | This file — new |
| `docs/phases/README.md` | New row + status |
| `docs/phases/phase-13-billing-stripe.md` | Updated 13c section + link to this file |
| `scripts/stripe-bootstrap/` | New standalone Go module (main.go, go.mod, AGENTS.md) |
| `internal/billing/stripe/client.go` | Stripe SDK wrapper with resilience |
| `internal/billing/stripe/webhook.go` | Webhook handler with signature verification + async drain |
| `internal/billing/stripe/service.go` | Connect-RPC BillingService handlers |
| `internal/billing/entitlements.go` | PlanEntitlements struct + resolver |
| `internal/billing/reconciler.go` | Periodic + reactive reconciler |
| `proto/limen/portal/v1/portal.proto` | BillingService RPCs |
| `internal/config/config.go` | BillingConfig struct |
| `config.yaml` | billing: section |
| migrations/ | tenant_billing, tenant_entitlements tables |

## Verification

- **Bootstrap idempotency**: run `stripe-bootstrap` twice against same Stripe account → second run is no-op, all IDs identical
- **Bootstrap convergence**: add a new feature to desired state → re-run → feature created and attached to products
- **Bootstrap archive**: remove a feature from desired state → re-run → feature archived (active: false), logged
- **Subscribe happy path**: tenant owner clicks "Upgrade to Team" → Stripe Checkout opens in test mode → completes with test card `4242 4242 4242 4242` → returns to portal with `status=trialing`, `plan=team`
- **Entitlement webhook — subscribe**: subscribe via Stripe → `entitlements.active_entitlement_summary.updated` fires → `tenant_entitlements` populated correctly → `plan` derived correctly
- **Entitlement webhook — upgrade**: upgrade from Developer to Team → webhook fires → entitlements updated → plan flips to 'team'
- **Entitlement webhook — cancel**: cancel Team → webhook fires with Developer features → entitlements reset → plan flips to 'developer'
- **Payment failure → grace**: trigger `invoice.payment_failed` → `grace_until` set → `invoice.payment_succeeded` → grace cleared
- **Cancel → reset**: cancel via Customer Portal → `customer.subscription.deleted` fires → plan resets to 'developer'
- **Webhook replay safety**: replay the same webhook twice → idempotent, no duplicate rows, no state corruption
- **Startup reconciliation**: simulate missed webhook → restart gateway → entitlements repaired from Stripe API
- **Resilience**: simulate Stripe 503 during quantity update → reconciler catches up on next periodic run

## Checklist

### 13c-a: Stripe Bootstrap Script

- [x] Create `scripts/stripe-bootstrap/` directory with standalone Go module
- [x] `main.go` with DesiredState struct populated with 2 Products, 3 Prices, 14 Features, attachments, webhook
- [x] `ensureProduct` helper: list + converge (create/update/archive) by name
- [x] `ensurePrice` helper: list + converge by lookup_key
- [x] `ensureFeature` helper: list + converge by lookup_key (archives not in desired state)
- [x] `ensureProductFeature` helper: list attachments + converge (attach missing)
- [x] `ensureWebhookEndpoint` helper: list + converge by URL
- [x] Archive logic: resources not in desired state → `active: false`, log warning
- [x] Output IDs to `.bootstrap-out.env` in KEY=VALUE format
- [x] `AGENTS.md` documenting: what it does, how to run, idempotency guarantees, adding new resources
- [ ] Makefile target: `make stripe-bootstrap` runs `go run ./scripts/stripe-bootstrap/`

### 13c-b: Backend Integration

- [ ] Add `github.com/stripe/stripe-go/v82` to `go.mod`
- [ ] `tenant_billing` Goose migration with RLS + partial unique + staff-mode SELECT clause
- [ ] `tenant_entitlements` Goose migration with RLS + (tenant_id, feature) unique + index
- [ ] `internal/billing/entitlements.go` — `PlanEntitlements` struct + `EntitlementsFromRows()` resolver
- [ ] `BillingConfig` struct in `internal/config/config.go`
- [ ] `billing:` section in `config.yaml` with Stripe keys, price IDs, trial_days, grace_days
- [ ] `internal/billing/stripe/client.go` — Stripe SDK wrapper with resilience
- [ ] Customer lookup/create by tenant ID with `tenant_public_id` metadata
- [ ] Checkout session creation with both line items (active_user + sa_connection prices)
- [ ] Customer Portal session creation
- [ ] `internal/billing/stripe/webhook.go` — handler at `/billing/stripe/webhook`
- [ ] Signature verification via `stripe.Webhook.ConstructEvent`
- [ ] Async drain: enqueue to in-memory channel, ACK immediately, drain goroutine mutates Postgres
- [ ] Event: `checkout.session.completed` — persist sub ID + price IDs, flip plan/status, run Reconcile
- [ ] Event: `customer.subscription.updated` — mirror status, period_end, cancel_at_period_end, prices
- [ ] Event: `customer.subscription.deleted` — set status='canceled', plan='developer', clear sub ID
- [ ] Event: `invoice.payment_failed` — set grace_until = now + grace_days
- [ ] Event: `invoice.payment_succeeded` — clear grace_until
- [ ] Event: `entitlements.active_entitlement_summary.updated` — parse lookup_keys, DELETE + UPSERT `tenant_entitlements`, derive `plan`
- [ ] `BillingService` proto in `proto/limen/portal/v1/portal.proto` + `buf generate`
- [ ] `GetBillingSummary` RPC: plan, status, active counts, publishable key
- [ ] `CreateCheckoutSession` RPC: Stripe Checkout with line items from billing metrics
- [ ] `OpenCustomerPortal` RPC: Stripe Customer Portal session
- [ ] `internal/billing/reconciler.go` — 1h jittered periodic loop
- [ ] Reconciler: reactive hook on billing metrics events
- [ ] Reconciler: upward-only quantities within billing month
- [ ] Reconciler: month-boundary reset to new month's count
- [ ] Startup reconciliation: List Active Entitlements API for active tenants
- [ ] Resilience config for Stripe endpoints in `config.yaml` resilience section
