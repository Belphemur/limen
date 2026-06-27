<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useBillingStore } from '@limen/shared/billing'

const store = useBillingStore()
const actionError = ref('')

onMounted(() => {
  void store.fetchBillingSummary()
})

// ---- ticker ----------------------------------------------------------
// `store.countdown` reads `Date.now()` inside a Vue computed, so it only
// re-evaluates when `graceUntil` changes — wall-clock movement is not a
// reactive dependency. We tick a local ref once a second and force the
// computed to re-derive from `store.graceUntil` so the rendered string
// actually counts down.
const tick = ref(0)
let timer: ReturnType<typeof setInterval> | null = null

onMounted(() => {
  timer = setInterval(() => {
    tick.value++
  }, 1000)
})

onBeforeUnmount(() => {
  if (timer === null) return
  clearInterval(timer)
  timer = null
})

const nextBillingDate = computed(() => {
  if (!store.currentPeriodEnd) return ''
  const ms = Date.parse(store.currentPeriodEnd)
  if (!Number.isFinite(ms)) return ''
  return new Date(ms).toLocaleDateString()
})

const localCountdown = computed(() => {
  // Reading tick.value is what makes this computed re-run every second;
  // without it, we'd be back to a cached value that never counts down.
  void tick.value
  const raw = store.graceUntil
  if (!raw) return 0
  const graceMs = Date.parse(raw)
  if (!Number.isFinite(graceMs)) return 0
  return graceMs - Date.now()
})

const graceCountdown = computed(() => formatCountdown(localCountdown.value))

const usageLimit = computed(() => (store.plan === 'developer' ? 1 : 'Unlimited'))

async function goToCheckout(): Promise<void> {
  actionError.value = ''
  try {
    const url = await store.createCheckoutSession(window.location.pathname)
    window.location.assign(url)
  } catch (err) {
    actionError.value = err instanceof Error ? err.message : String(err)
  }
}

async function goToPortal(): Promise<void> {
  actionError.value = ''
  try {
    const url = await store.openCustomerPortal(window.location.pathname)
    window.location.assign(url)
  } catch (err) {
    actionError.value = err instanceof Error ? err.message : String(err)
  }
}

function formatCountdown(ms: number): string {
  if (ms <= 0) return 'expired'
  const totalMinutes = Math.floor(ms / 60_000)
  const days = Math.floor(totalMinutes / (60 * 24))
  const hours = Math.floor((totalMinutes % (60 * 24)) / 60)
  if (days > 0) return `${days}d ${hours}h remaining`
  if (hours > 0) return `${hours}h remaining`
  const minutes = totalMinutes % 60
  return `${minutes}m remaining`
}
</script>

<template>
  <section class="mx-auto max-w-2xl space-y-4 p-6" data-testid="portal-billing-page">
    <h1 class="text-2xl font-bold">Billing</h1>

    <p v-if="store.isLoading" class="text-sm text-slate-500">Loading billing summary…</p>
    <p v-else-if="store.error" class="text-sm text-rose-600" role="alert">
      Could not load billing summary: {{ store.error }}
    </p>

    <!-- Grace warning -->
    <div
      v-if="store.bannerState === 'grace-amber'"
      class="rounded-md border border-amber-300 bg-amber-50 p-4 text-sm text-amber-900"
      role="status"
      data-testid="portal-billing-grace-warning"
    >
      Payment past due — {{ graceCountdown }}.
    </div>
    <div
      v-else-if="store.bannerState === 'expired-red'"
      class="rounded-md border border-rose-300 bg-rose-50 p-4 text-sm text-rose-900"
      role="alert"
      data-testid="portal-billing-expired"
    >
      Subscription is inactive. Update your payment method to restore access.
    </div>
    <div
      v-else-if="store.bannerState === 'canceling-amber'"
      class="rounded-md border border-amber-300 bg-amber-50 p-4 text-sm text-amber-900"
      role="status"
      data-testid="portal-billing-canceling"
    >
      Subscription cancels at the end of the current period.
    </div>
    <div
      v-else-if="store.bannerState === 'downgraded-info'"
      class="rounded-md border border-sky-300 bg-sky-50 p-4 text-sm text-sky-900"
      role="status"
      data-testid="portal-billing-downgraded"
    >
      Your Team subscription ended and the tenant was moved to the Developer plan. Re-upgrade any
      time to restore unlimited usage.
    </div>

    <!-- Plan card -->
    <div class="rounded-lg border border-slate-200 bg-white p-4 shadow-sm">
      <h2 class="text-lg font-semibold">Current Plan</h2>
      <p class="mt-2 text-sm text-slate-700">Plan: {{ store.plan || '—' }}</p>
      <p class="text-sm text-slate-700">Status: {{ store.status || '—' }}</p>
      <p v-if="nextBillingDate" class="text-sm text-slate-700">
        Next billing: {{ nextBillingDate }}
      </p>
      <p v-if="store.cancelAtPeriodEnd" class="text-sm text-slate-700">Cancels at period end.</p>
    </div>

    <!-- Usage card -->
    <div class="rounded-lg border border-slate-200 bg-white p-4 shadow-sm">
      <h2 class="text-lg font-semibold">Usage</h2>
      <p class="mt-2 text-sm text-slate-700">
        Active users: {{ store.activeUserCount }} / {{ usageLimit }}
      </p>
      <p class="text-sm text-slate-700">
        Service-account connections: {{ store.activeSaConnectionCount }} / {{ usageLimit }}
      </p>
    </div>

    <p v-if="actionError" class="text-sm text-rose-600" role="alert">{{ actionError }}</p>

    <div class="flex flex-wrap gap-3">
      <button
        type="button"
        class="cursor-pointer rounded-md border border-slate-200 bg-white px-3 py-2 text-sm font-medium text-slate-700 hover:border-slate-400"
        data-testid="portal-billing-manage-button"
        @click="goToPortal"
      >
        Manage in Stripe →
      </button>
      <button
        type="button"
        class="cursor-pointer rounded-md bg-slate-900 px-3 py-2 text-sm font-medium text-white hover:bg-slate-700"
        data-testid="portal-billing-upgrade-button"
        @click="goToCheckout"
      >
        Upgrade →
      </button>
    </div>
  </section>
</template>
