# Phase 13 — Billing with Stripe (per-seat)

**Depends on**: [Phase 4](phase-04-tenant-auth-session.md) (tenant + Zitadel grants), [Phase 9](phase-09-portal-spa.md) (portal RPC + SPA), [Phase 12](phase-12-staff-backoffice.md) (staff visibility), [Phase 11](phase-11-production-deployment.md) (prod secrets + Caddy).
**Unblocks**: turning the gateway from a free deployment into a SaaS.

## Goal

Charge customer tenants via [Stripe Billing](https://stripe.com/billing) using **per-seat subscriptions**: one subscription per tenant, with `quantity = active seat count` driven by the Zitadel user grants Limen already trusts ([Phase 4](phase-04-tenant-auth-session.md)). v1 is deliberately seat-only — no metered usage, no tiering by tool calls, no overage charges. Usage-based billing (per request, per tool call, per upstream) is called out as a future extension below but is **out of scope** for v1.

Out of the box this phase delivers:

- A tenant owner can subscribe their tenant by clicking through Stripe's hosted Checkout from the portal.
- Limen keeps Stripe in sync as the admin adds / removes / disables users in Zitadel.
- Limen reads subscription state from Stripe (via webhooks plus a periodic reconciler) and gates portal + MCP routes when a tenant is past-due, unpaid, or canceled.
- The tenant owner can self-serve via Stripe's hosted [Customer Portal](https://stripe.com/billing/customer-portal) (change card, view invoices, cancel) without leaving the Limen portal flow.
- The staff backoffice ([Phase 12](phase-12-staff-backoffice.md)) shows subscription state per tenant and can override (comp / extend grace / force-cancel) through audited RPCs.
- The reserved `_staff` tenant is **never** billed. Billing is opt-in per customer tenant; a tenant in `kind=customer` with no Stripe customer attached operates in a free-tier shape configured in `config.yaml` (e.g. capped at N users, MCP works but the portal nags).

## Non-goals (v1)

- Per-request / per-tool-call metered billing. Designed-for, not built.
- Per-upstream price differentiation. All upstreams cost the same; pricing is on seats only.
- Multiple plans / pricing tiers. v1 ships one product, one price (per currency). The schema accommodates more.
- Invoicing flows outside Stripe (PO / NET-30 / wire). Stripe-managed invoices only.
- Tax remediation outside Stripe Tax. We turn it on and trust Stripe's collection rules.

## Design

### Tenant ↔ Stripe Customer (1:1)

A new table `tenant_billing` keyed by `tenant_id` (one row per customer tenant, FK to `tenants`; staff tenant has no row):

```sql
CREATE TABLE tenant_billing (
  id                      BIGSERIAL PRIMARY KEY,
  public_id               TEXT NOT NULL UNIQUE,            -- bil_<ulid>
  tenant_id               BIGINT NOT NULL UNIQUE REFERENCES tenants(id),
  stripe_customer_id      TEXT,                             -- cus_...
  stripe_subscription_id  TEXT,                             -- sub_...
  stripe_price_id         TEXT,                             -- price_... (the seat price the sub is on)
  status                  TEXT NOT NULL DEFAULT 'none',     -- 'none' | 'trialing' | 'active' | 'past_due' | 'unpaid' | 'canceled' | 'incomplete'
  seat_count              INTEGER NOT NULL DEFAULT 0,       -- last-pushed Stripe quantity
  current_period_end      TIMESTAMPTZ,                      -- mirrored from Stripe
  cancel_at_period_end    BOOLEAN NOT NULL DEFAULT false,
  grace_until             TIMESTAMPTZ,                      -- past_due / unpaid soft-grace end
  last_synced_at          TIMESTAMPTZ,                      -- reconciler watermark
  created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at              TIMESTAMPTZ
);
```

Stripe is the source of truth for everything beyond `seat_count`; Limen mirrors the fields it needs for fast read-path gating without doing a Stripe round-trip on every request. `status` follows Stripe's [subscription status](https://stripe.com/docs/api/subscriptions/object#subscription_object-status) enum verbatim plus a synthetic `none` for "no subscription yet" — easier than dealing with nullable enums.

RLS: row is visible only when `tenant_id = current_setting('limen.tenant_id')::bigint`, plus the [Phase 12](phase-12-staff-backoffice.md) `limen.staff_mode = 'on'` clause on `SELECT`.

### Seat definition

A **seat** is a Zitadel user that holds a grant against the Limen project for this tenant's org. Concretely: anyone who can log in to `/t/<tenant>/portal/` and `/t/<tenant>/mcp` is a seat. Roles (`owner` / `admin` / `member`) do not matter for billing in v1 — all three count equally. The `super_admin` role lives only in the staff org and is **never** billed.

The seat count is computed from Zitadel, not from Limen's local user table: when an admin removes a grant in Zitadel (via the portal Members RPC, which already calls `UserService.RemoveUserGrant`), the seat count drops the next time the reconciler runs (and reactively, on the RPC itself — see below).

#### Reconciliation

Two layers, both writing through one path (`internal/billing/seats.go::Reconcile(tenantID)`):

1. **Reactive**: every portal Members-mutation RPC (`InviteMember`, `RemoveMember`, `UpdateRole`) and every login-time first-user-grant call fires a `Reconcile` for the tenant after the Zitadel call returns. Idempotent — if the computed seat count matches Stripe's, no-op.
2. **Periodic**: a background job (every 6 h, jittered) loops over `tenant_billing` rows with `status IN ('trialing','active','past_due')` and reconciles. Survives missed RPC hooks, missed webhooks, and Zitadel drift.

`Reconcile`:
- Counts user grants in Zitadel via `UserService.SearchUserGrants` filtered by `projectId` + `granted_org_id = tenant.zitadel_org_id`.
- If `count == tenant_billing.seat_count`, no-op (updates `last_synced_at`).
- Otherwise calls Stripe `SubscriptionItem.Update(itemID, quantity=count, proration_behavior='create_prorations')`. Stripe handles mid-cycle proration automatically.
- Updates `seat_count` + `last_synced_at` after Stripe ack.
- Wrapped in `internal/resilience.Client("stripe.subscription_items", cfg)` from [Phase 10](phase-10-wiring-hardening.md). Stripe outages do not block portal RPCs — the periodic loop catches up.

### Subscribe flow

```
SPA: "Subscribe" CTA on /t/<tenant>/portal/billing
  → SPA calls portal RPC CreateCheckoutSession()
  → Limen: ensure stripe_customer_id (create Customer if absent, name = tenant.name, metadata.tenant_id = <ulid>)
  → Limen: stripe.CheckoutSession.Create({
        mode='subscription',
        customer=<cus>,
        line_items=[{price=<config.seat_price_id>, quantity=<current seat count>}],
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
- Persists `stripe_subscription_id`, `stripe_price_id`, `status` (`trialing` or `active`), `current_period_end`.
- Runs `Reconcile` once to lock in the seat quantity (Checkout takes a starting quantity, but seats may have moved between Checkout-open and Checkout-complete).

### Customer Portal flow

`OpenStripePortal(tenant)` Connect RPC → `stripe.BillingPortalSession.Create({customer=<cus>, return_url='/t/<tenant>/portal/billing'})` → SPA redirects. The hosted portal handles: change payment method, view invoices, update billing address, cancel subscription. We do not reimplement any of that.

### Webhook endpoint

Mounted at `/billing/stripe/webhook` (root-level, no tenant prefix — Stripe doesn't know about tenants). Routing per event:

| Event                                                        | Action                                                                                                                          |
| ------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------- |
| `checkout.session.completed`                                 | Persist subscription id; flip status; `Reconcile` seats once.                                                                   |
| `customer.subscription.created` / `.updated`                 | Mirror `status`, `current_period_end`, `cancel_at_period_end`, `stripe_price_id`.                                               |
| `customer.subscription.deleted`                              | Set status `canceled`; clear `stripe_subscription_id`; keep `stripe_customer_id` for invoice history; gate routes on next req.  |
| `invoice.payment_failed`                                     | Set `grace_until = now + config.grace_days` (default 7); status mirrors (`past_due` / `unpaid`).                                |
| `invoice.payment_succeeded`                                  | Clear `grace_until`.                                                                                                            |
| `customer.subscription.trial_will_end`                       | Emit audit event; portal banner picks it up via subscription status.                                                            |

Signature verified with `stripe.Webhook.ConstructEvent`. Replay-safe: every handler is idempotent — we look up by Stripe object id and either insert or update. Webhook secret comes from `config.yaml` (`billing.stripe.webhook_secret`), wired through `${STRIPE_WEBHOOK_SECRET}` env-substitution.

The webhook handler is **not** behind a tenant or session middleware — but it is behind:

- HTTPS + the Caddy proxy (which strips inbound `X-Forwarded-*` Stripe doesn't set).
- The resilience-wrapped HTTP server with a request-size cap (Stripe events are < 100 KB).
- A 5-second handler timeout; the handler enqueues to an in-memory channel and ACKs immediately. The drain goroutine is what calls Stripe back / mutates Postgres. This keeps Stripe's 30 s ack deadline comfortable even when Postgres is slow.

### Gating

A new middleware `RequireBillingActive` lives in `internal/billing/middleware.go`:

```
read tenant_billing row → status ∈ {'trialing','active'}                                  → pass
                       → status ∈ {'past_due','unpaid'} ∧ now < grace_until               → pass with warning header (X-Limen-Billing: grace)
                       → status ∈ {'past_due','unpaid'} ∧ now ≥ grace_until               → 402 Payment Required (portal redirect for HTML, JSON for RPC, MCP error)
                       → status == 'canceled'                                              → 402 Payment Required
                       → status == 'none' ∧ free-tier limits ok                            → pass with X-Limen-Billing: free
                       → status == 'none' ∧ free-tier limits exceeded                      → 402 Payment Required
```

Mounted on:
- `/t/{tenant}/mcp` (the value-generating path) — gated.
- `/t/{tenant}/api/*` (portal RPCs) — gated **except** the billing sub-namespace (`limen.portal.v1.BillingService/*`) and the read-only Settings RPCs that the SPA needs to render the "your subscription expired" page. An admin always needs to be able to click "Pay now."
- The staff tenant is exempt.

Past-due tenants see the portal in degraded mode: a red banner at the top, billing page works, everything else is read-only.

### Free tier (status=`none`)

`config.yaml`:

```yaml
billing:
  enabled: true
  stripe:
    api_key: "${STRIPE_API_KEY}"
    webhook_secret: "${STRIPE_WEBHOOK_SECRET}"
    publishable_key: "${STRIPE_PUBLISHABLE_KEY}"   # public, surfaced to SPA
  seat_price_id: "price_..."
  trial_days: 14
  grace_days: 7
  free_tier:
    max_seats: 2
    max_upstream_links: 1
    portal_nag: true   # show "upgrade" banner in the SPA
```

If `billing.enabled: false` (self-hosters who don't want Stripe), the gating middleware short-circuits to pass-through and the portal billing page is hidden. The free-tier knobs apply only when billing is enabled but the tenant is `status='none'`.

### Portal SPA changes

New `/t/<tenant>/portal/billing` route:

- **No subscription yet**: shows plan card (one plan in v1: seat price × current-seat-count = monthly), seat-count preview, **Subscribe** button → `CreateCheckoutSession` → redirect.
- **Trialing**: badge "Trial — N days left", current seat count, **Manage subscription** button → `OpenStripePortal`.
- **Active**: current seat count, next-bill date, line-item preview from Stripe ($X.XX × N seats), **Manage subscription** button.
- **Past-due / unpaid**: red banner across the whole portal + page-level "Payment issue" card with "Update payment method" CTA into Customer Portal. Grace countdown.
- **Canceled**: paid-through-date card; **Resubscribe** button.

Connect-RPC additions to `proto/limen/portal/v1/portal.proto`:

```proto
service BillingService {
  rpc GetBillingSummary(google.protobuf.Empty) returns (BillingSummary);
  rpc CreateCheckoutSession(google.protobuf.Empty) returns (CheckoutSession);  // returns hosted URL
  rpc OpenCustomerPortal(google.protobuf.Empty) returns (CustomerPortal);       // returns hosted URL
}
```

Owner-only (interceptor: `RequireRole("owner")`). Admins and members do **not** see Billing in the nav.

### Staff backoffice integration

New `StaffService` RPCs ([Phase 12](phase-12-staff-backoffice.md) extension):

- `GetTenantBilling(tenant_id)` — full mirror of `tenant_billing` plus a recent-invoices list (via Stripe API, live).
- `ExtendGrace(tenant_id, until)` — bump `grace_until` past Stripe's value (overrides middleware). Audited.
- `CompTenant(tenant_id, until)` — write `status='active'` + `current_period_end=until` regardless of Stripe state. Audited. Used for comp accounts and during incident-response when Stripe is misbehaving.
- `ForceCancel(tenant_id, reason)` — cancel the Stripe sub immediately. Audited.

Backoffice **Tenants** detail card gains: current MRR contribution, plan, seat history sparkline, payment-method-on-file Yes/No, last-invoice status, grace state.

### Crypto / secrets

- `STRIPE_API_KEY` (server-side) and `STRIPE_WEBHOOK_SECRET` are mounted via `docker secret` (prod) or `.env` (dev). Never logged.
- `STRIPE_PUBLISHABLE_KEY` is public; surfaced to the SPA at runtime via a `/t/<tenant>/api/limen.portal.v1.BillingService/GetBillingSummary` field (saves the SPA from baking it into the build).
- No card data ever touches Limen. Checkout + Customer Portal are hosted; PCI scope stays Stripe-only.

### Resilience

Every Stripe call uses `internal/resilience.Client("stripe.<endpoint>", cfg)` ([Phase 10](phase-10-wiring-hardening.md)). Per-endpoint defaults table extended:

| Dependency                    | retries | base / max interval | breaker fails | breaker open |
| ----------------------------- | ------- | ------------------- | ------------- | ------------ |
| Stripe Checkout / Portal API  | 2       | 250 ms / 2 s        | 5             | 30 s         |
| Stripe Subscription update    | 3       | 500 ms / 5 s        | 5             | 60 s         |
| Stripe webhook → Postgres     | n/a     | n/a                 | n/a           | n/a (local)  |

A breaker-open on subscription updates does **not** drop seat changes — the periodic reconciler picks them up later. The Members RPC still succeeds on its own merits (Zitadel grant change committed); we log a structured warning.

## Deliverables

- New `internal/billing/` package:
  - `client.go` — Stripe SDK wrapper using the resilience client.
  - `seats.go` — `Reconcile(tenantID)`.
  - `webhook.go` — Stripe webhook handler + dispatcher.
  - `middleware.go` — `RequireBillingActive`.
  - `service.go` — Connect-RPC `BillingService` handlers.
- Migration: `tenant_billing` table + RLS policies + partial unique indexes.
- `proto/limen/portal/v1/portal.proto` — add `BillingService`.
- `proto/limen/staff/v1/staff.proto` — add `GetTenantBilling`, `ExtendGrace`, `CompTenant`, `ForceCancel`.
- `config.yaml` — `billing:` section.
- SPA: new `web/src/pages/Billing.vue`, billing banner component, nav item for owners only.
- Stripe Dashboard manual setup (documented in `docs/runbook.md`): product + price + webhook endpoint registration.
- `internal/resilience` defaults extended with Stripe entries.

## Verification

- **Subscribe happy path**: tenant owner clicks Subscribe → Stripe Checkout opens in test mode → completes with test card `4242 4242 4242 4242` → returns to portal with `status=trialing` → `seat_count` matches Zitadel grant count.
- **Seat reconciliation reactive**: admin removes a member; within the same request lifecycle Stripe's subscription item quantity drops by 1; next month's invoice reflects proration.
- **Seat reconciliation periodic**: manually flip a Zitadel grant via `zitadel-cli`, wait for the 6 h job (or trigger manually via `limen billing reconcile <tenant>`) → quantity updates.
- **Webhook signature**: post an unsigned event → 400. Post a valid event with a tampered body → 400. Post a valid event twice → second is a no-op (idempotency).
- **Payment failure → grace**: trigger `invoice.payment_failed` from Stripe CLI → `grace_until` set 7 days out → portal banner shows → MCP still works during grace → after grace expires (or force `now > grace_until`) → MCP returns 402.
- **Payment recovery**: trigger `invoice.payment_succeeded` after a `payment_failed` → grace cleared → banner gone.
- **Cancel**: tenant owner cancels via Customer Portal → `cancel_at_period_end=true` → at `current_period_end` Stripe fires `customer.subscription.deleted` → status flips to `canceled` → MCP returns 402.
- **Staff comp**: `super_admin` calls `CompTenant(tenant_id, +30d)` → tenant routes pass even with no Stripe sub → audit row written.
- **Free-tier limits**: `status='none'` tenant adds a third member → Members RPC returns 402 with structured error `billing.free_tier.max_seats`.
- **Staff tenant exempt**: log in to `/t/_staff/portal/` with no Stripe configured → backoffice loads cleanly.
- **`billing.enabled: false`**: portal nav has no Billing item; gating middleware is a pass-through; staff backoffice billing card shows "billing disabled."

## Risks

- **Stripe outage during seat changes**: mitigated by the periodic reconciler — quantity drift heals on its own. Verified in tests by simulating a 503 from Stripe and confirming the next reconcile run repairs the count.
- **Webhook ordering**: Stripe can deliver webhooks out of order. Mitigation: handlers are state-mirroring (not state-mutating-by-delta), so a late `subscription.updated` containing an older `current_period_end` is detected via `created` timestamp comparison and dropped.
- **Proration surprises**: large mid-cycle seat swings can cause customer-visible $X.XX proration line items. Documented in the FAQ; the Members RPC has a UI confirmation when delta > 5 seats.
- **Tax compliance scope**: Stripe Tax handles collection but operators must configure their nexuses. Runbook calls this out.
- **Customer Portal feature flag drift**: Stripe periodically gates Customer Portal features behind dashboard toggles. Bootstrap script for Stripe configuration (or a one-time runbook step) enumerates the required toggles.
- **Pricing model change later**: moving from seat-only to seat-plus-usage means adding a `usage_records` Stripe item and a metering pipeline. The `tenant_billing` schema already names `stripe_price_id` singularly — when we add usage, we'll attach a second subscription item (Stripe supports multi-item subs). No table rename needed.

## Future: usage-based billing (deferred)

Sketch for when we revisit:

- New table `usage_event(tenant_id, user_id, kind, ts, qty)` written from inside `Gateway.CallTool`. Kinds: `mcp_request`, `tool_call`, `upstream_call`. Append-only, partitioned by month.
- Hourly aggregator that batches `usage_event` into Stripe `UsageRecord.Create(subscription_item, quantity)` against a metered subscription item on the same subscription.
- Stripe shows usage on invoices alongside the per-seat line.
- Operator can pick: pure seat, seat + metered, or pure metered, per-tenant or globally.

This stays out of v1 because it adds: a hot-path write per tool call, an aggregation/idempotency layer, and substantially more support-burden surface ("why is my bill X?"). Seat-based is enough to start charging.

## Checklist

- [ ] `tenant_billing` migration with RLS policies + partial unique on `(tenant_id) WHERE deleted_at IS NULL` + the staff-mode SELECT clause from Phase 12
- [ ] `internal/billing/` package: client, seats, webhook, middleware, service
- [ ] Stripe SDK (`github.com/stripe/stripe-go/v82`) added to `go.mod`; SDK calls go through `internal/resilience.Client("stripe.<endpoint>", cfg)`
- [ ] `proto/limen/portal/v1/portal.proto` extended with `BillingService` (owner-only)
- [ ] `proto/limen/staff/v1/staff.proto` extended with `GetTenantBilling`, `ExtendGrace`, `CompTenant`, `ForceCancel` (all audited)
- [ ] `RequireBillingActive` middleware mounted on `/t/{tenant}/mcp` and `/t/{tenant}/api/*` except the billing namespace; staff tenant exempt
- [ ] Webhook handler at `/billing/stripe/webhook` with Stripe signature verification, idempotency by Stripe object id, async drain
- [ ] Seat reconciler: reactive hook after every Members-mutation RPC + 6 h jittered periodic loop
- [ ] Free-tier limits enforced in `Members.Invite` and `Upstream.AddLink` paths when `status='none'`
- [ ] SPA `Billing.vue` page + global past-due banner + nav item gated on `role=owner`
- [ ] `config.yaml` `billing:` section with `enabled`, Stripe key/secret refs, `seat_price_id`, `trial_days`, `grace_days`, `free_tier.*`
- [ ] Stripe Dashboard runbook: product + seat price + webhook endpoint + Customer Portal toggles + Tax configuration
- [ ] `staff_audit_log` records `billing.comp`, `billing.extend_grace`, `billing.force_cancel` actions with reason
- [ ] Integration tests using the Stripe test mode + Stripe CLI for webhook replay covering every Verification scenario
- [ ] `billing.enabled: false` short-circuits the middleware and hides the portal nav item — self-host path stays free
