---
phase: "13e"
title: "Portal Billing Page + Status Banner"
status: in_progress
progress: 30
depends_on: ["13c", "13d"]
updated: "2026-06-26"
---

# Phase 13e — Portal Billing Page + Status Banner

**Depends on**: Phase 13c (Stripe Integration — `BillingService` RPCs), Phase 13d (Plan Enforcement — entitlement state machine + `X-Limen-Billing` middleware header).  
**Unblocks**: nothing planned.

## Goal

Give tenants direct visibility into their billing status. A `/billing` page in the portal SPA shows the active plan, usage counters, period end, and CTAs to open the Stripe Customer Portal or create a Checkout session. A reactive `BillingBanner` component surfaces immediate warnings (grace period, past-due, cancellation, auto-downgrade) in **both** the portal and admin SPAs so an `owner` never misses a payment state change.

## Background

Phase 13d implemented plan enforcement: tenants whose subscription is `past_due` / `unpaid` / `incomplete` / `paused` get a 402 at the gateway, and a one-time auto-downgrade to the Developer plan happens on cancellation. But there is **no in-product surface** that tells the tenant what happened. A 402 alone does not say "your card declined and you have 7 days to fix it." Stripe sends the customer emails, but the portal must echo that state in the UI it owns.

Phase 13c shipped the `BillingService` RPCs (`GetBillingSummary`, `CreateCheckoutSession`, `OpenCustomerPortal`) and the Go-side middleware that stamps `X-Limen-Billing: grace` on responses during the grace window. This phase consumes both.

## Design

### Component map

```
                ┌──────────────────────────────────────────────┐
                │  Go  ──────────────────────────────────────► │
                │   • BillingInterceptor stamps X-Limen-Billing│
                │   • GetBillingSummary RPC returns full state │
                │   • 60s grace window middleware             │
                │                                              │
                │  TS  ◄──────────────────────────────────────  │
                │   • useBillingStore (Pinia)                  │
                │   • billingHeaderFetch wrapper               │
                │   • BillingBanner.vue (4 states + countdown) │
                │   • BillingPage.vue (plan + usage + CTAs)    │
                └──────────────────────────────────────────────┘
```

### Shared billing module (`web/shared/src/billing/`)

Mirrors the `web/shared/src/session/` structure so portal + admin can share one transport and one store:

```ts
// web/shared/src/billing/billingClient.ts — pin pattern
let transport: Transport | null = null
export function setBillingTransport(t: Transport) { transport = t }
export function resetBillingTransport() { transport = null }
export function createBillingClient(over?: Transport): BillingService {
  return createPromiseClient(BillingService, over ?? transport ?? throwNoTransport())
}
```

```ts
// web/shared/src/stores/useBillingStore.ts — Pinia store
state: plan, status, activeUserCount, activeSaConnectionCount,
       stripePublishableKey, currentPeriodEnd, cancelAtPeriodEnd,
       graceUntil, isLoading, error
getters:
  bannerState: 'none' | 'grace-amber' | 'expired-red'
             | 'canceling-amber' | 'downgraded-info'   // exhaustive
  needsAttention = bannerState !== 'none'
  countdown  → ms remaining (0 on past/empty/unparseable)
actions:
  fetchBillingSummary()           // swallows errors into `error` (banner is supplementary)
  createCheckoutSession(returnTo) // GET → window.location = url
  openCustomerPortal(returnTo)   // GET → window.location = url
  handleHeaderSignal(signal)      // fire-and-forget, called by interceptor
```

The `bannerState` getter is the only place that reads `(status, plan, cancelAtPeriodEnd, graceUntil)`. It is exhaustive over the four inputs and returns one of five strings; a TypeScript switch is the implementation so the compiler flags any future state added without a branch.

### Reactive plumbing

The Go middleware stamps `X-Limen-Billing: grace` on every response while the tenant is in the grace window. The `billingHeaderFetch` wrapper in each SPA peeks that header on every response and dynamically imports `@limen/shared/billing` to call `useBillingStore().handleHeaderSignal('grace')` — a fire-and-forget ping that forces a re-`fetchBillingSummary` so the banner can update with fresh `grace_until` without a full page reload. The dynamic import is deliberate: it keeps the Pinia store out of the SSR / test-render code paths when no header is ever stamped.

A 60-second `setInterval` poll is the fallback for the case where the header is absent on every response (e.g., the user never made a request after the grace window started — for instance, they were idle). The poll is started once on SPA boot by `main.ts` and is cancelled on logout.

### Banner states

| State              | Trigger                                                       | Tone      | CTA                                                |
| ------------------ | ------------------------------------------------------------- | --------- | -------------------------------------------------- |
| `grace-amber`      | `status ∈ {past_due, unpaid}` AND `graceUntil > now`          | amber     | "Update payment" → `openCustomerPortal`            |
| `expired-red`      | `status ∈ {past_due, unpaid}` AND `graceUntil ≤ now`          | red       | "Resubscribe" → `createCheckoutSession`            |
| `canceling-amber`  | `cancelAtPeriodEnd == true` AND `status == active`            | amber     | "Resume" → `openCustomerPortal`                    |
| `downgraded-info`  | `plan == developer` AND `status == canceled`                 | blue/info | "Upgrade" → `createCheckoutSession`                |
| `none`             | all other combinations                                        | —         | banner hidden                                      |

The banner is rendered above the `<RouterView>` in both `App.vue` shells (portal and admin) and is gated on `needsAttention`. The countdown next to "X days, Y hours remaining" is driven by the `countdown` getter — recomputed on a 1-second `setInterval` started when the banner mounts.

### Page: `BillingPage.vue` (`/billing` in portal SPA)

Three sections stacked vertically, single column on mobile, two-column on `md+`:

1. **Plan card** — current plan name, status pill (Active / Past Due / Canceled / Developer), `currentPeriodEnd` formatted in the tenant's locale, `cancelAtPeriodEnd` warning if set, "Manage subscription" button → `openCustomerPortal`.
2. **Usage counters** — two cards: `activeUserCount` / `MaxActiveUsers` (filled bar, with a note "Stripe counts this on the 1st of the month"), `activeSaConnectionCount` / `MaxSAConnections` (live number, peak concurrent in the current period).
3. **Upgrade / billing-history CTAs** — if `plan == developer` or `status == canceled`, a primary "Upgrade to Team" button → `createCheckoutSession`. If subscribed, a secondary "Open Stripe Customer Portal" link for invoice history.

Route is owner-only: the sidebar link is hidden for `admin` / `member`. The page itself shows a "Billing is only available to owners" notice for non-owners so the route guard can be a simple `role === 'owner'` check, not a redirect.

### File layout

```
web/shared/src/billing/
├── index.ts                   # re-exports client + store
├── billingClient.ts           # setBillingTransport / reset / create
└── stores/
    └── useBillingStore.ts

web/shared/src/billing/
└── (the store is exposed as @limen/shared/billing and re-exported)

web/shared/package.json        # adds "./billing" subpath export

web/portal/src/pages/BillingPage.vue
web/portal/src/components/BillingBanner.vue
web/portal/src/router/index.ts # adds /billing route
web/portal/src/api/client.ts   # portalTransport uses billingHeaderFetch

web/admin/src/components/BillingBanner.vue
web/admin/src/transport/adminClient.ts   # perTenantTransport uses billingHeaderFetch
web/admin/src/main.ts                     # fires useBillingStore().fetchBillingSummary()

web/portal/src/main.ts                    # fires useBillingStore().fetchBillingSummary()
```

## Deliverables

### 13e-a: Pinia store + reactive banner (both SPAs)

| File | Purpose |
|------|---------|
| `web/shared/src/billing/billingClient.ts` | `setBillingTransport` / `resetBillingTransport` / `createBillingClient(transport?)` — module-level pin, mirrors `sessionClient.ts` |
| `web/shared/src/stores/useBillingStore.ts` | Pinia store: state, `bannerState` (exhaustive), `needsAttention`, `countdown`, `fetchBillingSummary`, `createCheckoutSession`, `openCustomerPortal`, `handleHeaderSignal` |
| `web/shared/src/billing/index.ts` | Re-exports client + store |
| `web/shared/package.json` | Adds `"./billing": "./src/billing/index.ts"` subpath export |
| `web/shared/src/api/billingHeaderFetch.ts` | `billingHeaderFetch(input, init)` — wraps cookie fetch, peeks `X-Limen-Billing: grace`, dynamic-imports `@limen/shared/billing`, calls `useBillingStore().handleHeaderSignal()`. Dynamic import keeps Pinia out of test renders. |
| `web/portal/src/api/client.ts` | `portalTransport` now uses `billingHeaderFetch` (covers `SessionService` + `PortalService` + `BillingService`) |
| `web/admin/src/transport/adminClient.ts` | Per-tenant `buildAdminTransport` uses `billingHeaderFetch`; signup transport stays plain cookie fetch (no tenant, no header) |
| `web/portal/src/main.ts` | After Pinia install: pin the same per-tenant transport to `session` + `billing` stores, fire `void useBillingStore().fetchBillingSummary()` |
| `web/admin/src/main.ts` | Same boot sequence; dedupes the triple `createConnectTransport` into a single `perTenantTransport` const |
| `web/portal/src/components/BillingBanner.vue` | 4 states + countdown, `setInterval(1s)` for live ticks, dismissable only on `downgraded-info` |
| `web/admin/src/components/BillingBanner.vue` | Same component, identical props |
| `web/portal/src/App.vue` | Renders `<BillingBanner v-if="needsAttention" />` above `<RouterView>` |
| `web/admin/src/App.vue` | Same wiring on the admin shell |

### 13e-b: Portal `/billing` page

| File | Purpose |
|------|---------|
| `web/portal/src/pages/BillingPage.vue` | Plan card + usage counters + CTAs; owner-only route guard; reads reactive store (no second `GetBillingSummary` call) |
| `web/portal/src/router/index.ts` | Adds `/billing` route, requires `role === 'owner'` |
| `web/portal/src/components/Sidebar.vue` | Hides "Billing" link for `admin` / `member` |
| `web/portal/tests/e2e/billing.spec.ts` | Playwright spec: navigate to `/billing`, see plan card, click "Open Customer Portal" sees a 302 (or stubbed response) |

### Behavioural contract

- `X-Limen-Billing: grace` is the **only** header the SPA cares about; absence does not break anything (boot-time `fetchBillingSummary` still runs).
- 60s polling fallback runs **only** when a tenant transport is pinned (signed-in tenant). Logout tears down the transport and the interval.
- Banner countdown is ms-based; on past / empty / unparseable `graceUntil`, `countdown` returns `0` and the banner falls through to `expired-red` so the user sees "Resubscribe" rather than a negative number.
- `fetchBillingSummary` is fire-and-forget; the banner is supplementary, not a hard gate. Failures land in `error` and are shown as a small "Refresh" link in the banner footer.

## Checklist

### 13e-a: Pinia store + reactive banner

- [ ] `web/shared/src/billing/billingClient.ts` — `setBillingTransport` / `resetBillingTransport` / `createBillingClient(transport?)` with module-level pin; throws on missing transport when no override is given
- [ ] `web/shared/src/stores/useBillingStore.ts` — Pinia store with all state fields, `bannerState` exhaustive over `(status, plan, cancelAtPeriodEnd, graceUntil)`, `needsAttention`, `countdown` (0 on past/empty/unparseable)
- [ ] Actions implemented: `fetchBillingSummary` (swallows errors into `error`), `createCheckoutSession(returnTo)`, `openCustomerPortal(returnTo)`, `handleHeaderSignal`
- [ ] `web/shared/src/billing/index.ts` re-exports client + store
- [ ] `web/shared/package.json` adds `"./billing": "./src/billing/index.ts"` subpath export
- [ ] `buf.gen.billing-ts.yaml` (modeled after `buf.gen.session-ts.yaml`) generates `proto/limen/portal` into `web/shared/src/gen` so the shared store can `import { BillingService }` without reaching into `web/portal` or `web/admin` (dependency direction preserved)
- [ ] `web/shared/src/api/billingHeaderFetch.ts` — wraps cookie fetch, peeks `X-Limen-Billing: grace`, dynamic-imports `@limen/shared/billing`, calls `useBillingStore().handleHeaderSignal()`. Dynamic import keeps Pinia out of test renders.
- [ ] `web/portal/src/api/client.ts` — `portalTransport` uses `billingHeaderFetch` (covers `SessionService` + `PortalService` + `BillingService`)
- [ ] `web/admin/src/transport/adminClient.ts` — per-tenant `buildAdminTransport` uses `billingHeaderFetch`; signup transport stays plain cookie fetch (no tenant, no header)
- [ ] `web/portal/src/main.ts` pins the per-tenant transport to `session` + `billing`, then fires `void useBillingStore().fetchBillingSummary()`
- [ ] `web/admin/src/main.ts` pins `session` + `billing` + `admin` to one `perTenantTransport` const, then fires `void useBillingStore().fetchBillingSummary()`; dedupes the three `createConnectTransport` calls
- [ ] 60s polling fallback started in both `main.ts` files, torn down on logout
- [ ] `BillingBanner.vue` (portal + admin) — 4 states rendered, `setInterval(1s)` for live ticks, dismissable only on `downgraded-info`, "Refresh" link in footer on `error`
- [ ] `<BillingBanner v-if="needsAttention" />` mounted above `<RouterView>` in both `App.vue` shells
- [ ] `vue-tsc --noEmit` clean in both SPAs; shared billing module type-checks clean under a temp tsconfig
- [ ] ESLint clean on changed files
- [ ] Shared vitest pass (store unit tests for `bannerState` matrix + `countdown`)

### 13e-b: Portal `/billing` page

- [ ] `web/portal/src/pages/BillingPage.vue` — plan card, usage counters (active users / max active users, peak SA connections / max SA connections), period end, CTAs
- [ ] Owner-only route guard: `role === 'owner'` or render a "Billing is only available to owners" notice (no redirect)
- [ ] `web/portal/src/router/index.ts` adds `/billing` route
- [ ] `web/portal/src/components/Sidebar.vue` — "Billing" link hidden for `admin` / `member`, visible for `owner`
- [ ] "Open Customer Portal" / "Resubscribe" / "Upgrade" CTAs call the right store action with `returnTo` set to `/billing`
- [ ] Reads reactive store (no second `GetBillingSummary` call on mount)
- [ ] `web/portal/tests/e2e/billing.spec.ts` Playwright spec: navigate to `/billing` as owner, see plan card + usage bars, click "Open Customer Portal" sees a 302 / stubbed redirect
- [ ] `vue-tsc --noEmit`, ESLint clean
- [ ] `pnpm test` and `pnpm build` green in both SPAs

## Design Decisions

### Why a shared module under `web/shared/src/billing/`

Both SPAs (portal + admin) need the same store + the same transport. Putting the store in either SPA would create a one-way dependency arrow that the other direction would eventually need to invert (admin would import from portal, or vice versa). Mirroring the `web/shared/src/session/` structure keeps the dependency graph flat.

### Why `bannerState` is a single exhaustive getter

The 5-way state machine is small but high-stakes — getting it wrong means a paying customer never sees the "Update payment" banner. Concentrating the logic in one `bannerState` computed (with a TypeScript switch) means there is one place to read for correctness and one place to unit test. The runtime `bannerState` lookup also feeds `needsAttention`, so the template stays a one-liner.

### Why `countdown` is a getter, not stored state

A `Date.now() - graceUntil` diff is already "live" by the time the template reads it. Storing it would require a global tick interval. The template `setInterval(1s)` already running for the live-tick is what keeps it fresh; storing it would just add a source of staleness when the banner mounts/unmounts.

### Why `fetchBillingSummary` swallows errors

The banner is supplementary. If `GetBillingSummary` fails (network blip, transient backend issue, RBAC glitch), the user should still see the rest of the app working. A failure lands in the store's `error` ref and renders as a small "Refresh" link in the banner footer — the store self-recovers on the next click. The boot-time call is also fire-and-forget (`void useBillingStore().fetchBillingSummary()`) for the same reason.

### Why 60s polling is a fallback, not the primary

The `X-Limen-Billing: grace` header is stamped on every response during the grace window, so an actively-using tenant is informed within one request. The 60s poll is the safety net for the idle case (user leaves the tab open, no requests fire for hours, grace window starts). It runs once and is cancelled on logout.

## Verification

- **Unit tests**: shared vitest covers the `bannerState` matrix (4 inputs × 5 outputs = 20+ cases) and `countdown` (past, future, empty string, unparseable string, `null`).
- **Type-check**: `vue-tsc --noEmit` clean in portal and admin; the shared billing module type-checks under a temporary tsconfig.
- **Lint**: ESLint clean on every changed file.
- **E2E**: `web/portal/tests/e2e/billing.spec.ts` covers owner happy path, non-owner notice, and a stubbed "Open Customer Portal" redirect.
- **Manual**: cancel a Team subscription from the Stripe Customer Portal → refresh the portal → `X-Limen-Billing: grace` (if still inside the window) or `canceling-amber` banner appears within one poll interval; click "Resume" → redirected to the Customer Portal; complete the resume in Stripe → the next request from the SPA clears the banner.
- **No backend changes**: this phase is SPA-only. The `BillingService` RPCs and the `X-Limen-Billing` header are already shipped in 13c / 13d.
