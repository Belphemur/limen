import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { createBillingClient } from '@shared/billing/billingClient.ts'

// BannerState is the UI-facing verdict the rest of the SPA renders
// against. It collapses the (status, plan, graceUntil) tuple from
// GetBillingSummary into one of five visual modes so consumers don't
// have to re-derive the state machine in every component.
//
//   'none'            — developer plan, no subscription, or active
//                       Team plan. No banner needed.
//   'grace-amber'     — Team plan in a Stripe grace window
//                       (past_due / unpaid with a future graceUntil).
//                       The tenant can still use the product; warn
//                       that access will end on graceUntil.
//   'expired-red'     — Stripe status is incomplete / incomplete_expired
//                       / paused. Access is blocked server-side; the
//                       banner is the SPA's only chance to tell the
//                       user why every action is failing.
//   'canceling-amber' — Team plan with cancel_at_period_end=true.
//                       The subscription is still active; remind the
//                       owner they'll downgrade at current_period_end.
//   'downgraded-info' — Plan == "developer" with a canceled status
//                       that was just auto-downgraded. The banner is
//                       informational: "you were moved to the free
//                       plan, here's how to come back."
export type BannerState =
  | 'none'
  | 'grace-amber'
  | 'expired-red'
  | 'canceling-amber'
  | 'downgraded-info'

// isFiniteState guards the math in `countdown`. The store keeps
// `graceUntil` as the raw RFC3339 string so the server is the source
// of truth; the computed derives a ms-since-epoch delta from it.
// Invalid / empty strings are treated as "no countdown" rather than
// NaN poisoning the rest of the UI.
function isFiniteNumber(n: number): boolean {
  return Number.isFinite(n)
}

// parseGraceMs converts an RFC3339 string to a millisecond timestamp.
// Returns null for empty / unparseable input so the computed can
// branch on "no grace" without try/catching in the hot path.
function parseGraceMs(raw: string): number | null {
  if (!raw) return null
  const ms = Date.parse(raw)
  return isFiniteNumber(ms) ? ms : null
}

export const useBillingStore = defineStore('billing', () => {
  // ---- raw wire state -------------------------------------------------
  // These mirror GetBillingSummaryResponse field-for-field. We keep the
  // snake-case-from-proto camelCase names so the store is a thin
  // pass-through; consumers can `store.plan === 'team'` without
  // remapping.
  const plan = ref('')
  const status = ref('')
  const activeUserCount = ref(0)
  const activeSaConnectionCount = ref(0)
  const stripePublishableKey = ref('')
  const currentPeriodEnd = ref('')
  const cancelAtPeriodEnd = ref(false)
  const graceUntil = ref('')

  // ---- request lifecycle ---------------------------------------------
  const isLoading = ref(false)
  const error = ref('')
  // lastFetchedAt is the wall-clock timestamp of the most recent
  // fetchBillingSummary call. handleHeaderSignal throttles against
  // it so a tenant sitting in a grace window doesn't fan out one
  // extra billing RPC per API request — during grace, every
  // response carries `X-Limen-Billing: grace` and the SPA's
  // response-header interceptor calls handleHeaderSignal on every
  // one. 30s is short enough to catch a status flip quickly, long
  // enough to collapse the burst of per-request signals into a
  // single refresh.
  const lastFetchedAt = ref(0)

  // ---- derived UI state ----------------------------------------------
  // bannerState is the single computed the rest of the app reads. It's
  // exhaustive over the (status, plan, cancel_at_period_end) tuple so
  // adding a new visual mode is a one-place change.
  const bannerState = computed<BannerState>(() => {
    // Blocked: server returns these for subscriptions that never
    // completed Checkout or were paused. The middleware rejects every
    // request; the banner is the only signal the user gets.
    if (
      status.value === 'incomplete' ||
      status.value === 'incomplete_expired' ||
      status.value === 'paused'
    ) {
      return 'expired-red'
    }

    // Grace: past_due / unpaid with a future grace deadline. The
    // server still serves traffic until graceUntil; we tell the user
    // access is about to end. A null/past grace means the server has
    // already cut us off; fall through to expired-red so the banner
    // actually shows the block state instead of resolving to 'none'.
    if (status.value === 'past_due' || status.value === 'unpaid') {
      const graceMs = parseGraceMs(graceUntil.value)
      if (graceMs !== null && graceMs > Date.now()) {
        return 'grace-amber'
      }
      return 'expired-red'
    }

    // Canceling: active Team subscription that will downgrade at
    // period end. Distinct from "grace" because there's no urgency —
    // the user explicitly canceled and the timer is current_period_end.
    if (
      plan.value === 'team' &&
      status.value === 'active' &&
      cancelAtPeriodEnd.value
    ) {
      return 'canceling-amber'
    }

    // Auto-downgrade notification: the server just moved this tenant
    // from team back to developer (canceled status, developer plan).
    // Show a soft "you were downgraded" banner with a re-up CTA.
    if (status.value === 'canceled' && plan.value === 'developer') {
      return 'downgraded-info'
    }

    return 'none'
  })

  // needsAttention is the cheap "should the banner be visible?" check
  // every layout component reaches for. `bannerState !== 'none'` is the
  // only correct predicate; we expose it as a named computed so the
  // intent is clear at the call site.
  const needsAttention = computed(() => bannerState.value !== 'none')

  // countdown is the ms remaining until graceUntil. Returns 0 when
  // graceUntil is empty / past / unparseable so consumers can render
  // "grace expired" without a null check.
  const countdown = computed(() => {
    const graceMs = parseGraceMs(graceUntil.value)
    if (graceMs === null) return 0
    const remaining = graceMs - Date.now()
    return remaining > 0 ? remaining : 0
  })

  // ---- actions --------------------------------------------------------
  // fetchBillingSummary pulls the current billing state from the
  // server. Callers should treat the store as the single source of
  // truth — components read `plan` / `bannerState` and never call
  // BillingService directly.
  async function fetchBillingSummary(): Promise<void> {
    // Stamp lastFetchedAt before issuing the RPC so an in-flight
    // request already counts toward the throttle window. Without
    // this, two concurrent callers could both pass the gate in
    // handleHeaderSignal and double-fetch.
    lastFetchedAt.value = Date.now()
    isLoading.value = true
    error.value = ''
    try {
      const resp = await createBillingClient().getBillingSummary({})
      plan.value = resp.plan
      status.value = resp.status
      activeUserCount.value = resp.activeUserCount
      activeSaConnectionCount.value = resp.activeSaConnectionCount
      stripePublishableKey.value = resp.stripePublishableKey
      currentPeriodEnd.value = resp.currentPeriodEnd
      cancelAtPeriodEnd.value = resp.cancelAtPeriodEnd
      graceUntil.value = resp.graceUntil
    } catch (err) {
      // We swallow the error string into `error` rather than throwing
      // because the billing banner is supplementary — a failed fetch
      // shouldn't crash the SPA. The header interceptor will retry on
      // the next gated request, so transient failures self-heal.
      error.value = err instanceof Error ? err.message : String(err)
    } finally {
      isLoading.value = false
    }
  }

  // createCheckoutSession asks the server for a Stripe Checkout URL
  // and hands it back to the caller (typically a "Upgrade" button)
  // for navigation. The returnTo is the SPA-relative path the browser
  // should land on after Stripe redirects back.
  async function createCheckoutSession(returnTo: string): Promise<string> {
    const resp = await createBillingClient().createCheckoutSession({ returnTo })
    return resp.redirectUrl
  }

  // openCustomerPortal asks the server for a Stripe Customer Portal
  // URL and hands it back for navigation. The portal is the only place
  // a Team subscriber can change payment method, cancel, or download
  // invoices — the SPA never collects card details directly.
  async function openCustomerPortal(returnTo: string): Promise<string> {
    const resp = await createBillingClient().openCustomerPortal({ returnTo })
    return resp.redirectUrl
  }

  // handleHeaderSignal is called by the response-header interceptor in
  // each SPA's transport. The server stamps `X-Limen-Billing: grace`
  // on every response that was served during a grace window; we treat
  // that as a hint to refresh the cached summary so the banner appears
  // without the user navigating away.
  //
  // Throttled to once per 30s per store instance: during grace, every
  // API response carries the header, so a naive implementation would
  // fan out one extra billing RPC per request. 30s is short enough to
  // catch a status flip promptly and long enough to collapse the
  // per-request burst into a single refresh.
  //
  // The function is fire-and-forget: the interceptor can't await
  // fetchBillingSummary (that would break streaming Connect responses
  // and reorder promise resolution), so we kick it off and let the
  // store's own loading flags track the in-flight refresh.
  function handleHeaderSignal(): void {
    if (Date.now() - lastFetchedAt.value < 30_000) {
      return
    }
    void fetchBillingSummary()
  }

  return {
    // state
    plan,
    status,
    activeUserCount,
    activeSaConnectionCount,
    stripePublishableKey,
    currentPeriodEnd,
    cancelAtPeriodEnd,
    graceUntil,
    isLoading,
    error,
    lastFetchedAt,
    // computed
    countdown,
    bannerState,
    needsAttention,
    // actions
    fetchBillingSummary,
    createCheckoutSession,
    openCustomerPortal,
    handleHeaderSignal,
  }
})
