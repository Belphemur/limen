<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { X } from '@lucide/vue'

// The store is the single source of truth for billing state. We import
// the type so the local helper can stay narrow — every branch in the
// `variant` switch is checked against the same union the store emits.
import { useBillingStore, type BannerState } from '@shared/stores/useBillingStore.ts'

const store = useBillingStore()

// ---- dismiss state ---------------------------------------------------
// `canceling-amber` and `downgraded-info` are soft warnings the user
// has already seen; the banner should get out of the way. `grace-amber`
// and `expired-red` cannot be dismissed — the user must resolve the
// underlying Stripe state. We reset `dismissed` on any state change
// so a fresh alert gets a fresh chance to be acknowledged.
const dismissed = ref(false)

watch(
  () => store.bannerState,
  () => {
    dismissed.value = false
  }
)

// visible is the single predicate the template renders against. We
// fold "should we even show a banner?" and "did the user dismiss this
// one?" into one computed so the template stays declarative.
const visible = computed(() => {
  if (store.bannerState === 'none') return false
  if (
    dismissed.value &&
    (store.bannerState === 'canceling-amber' ||
      store.bannerState === 'downgraded-info')
  ) {
    return false
  }
  return true
})

// ---- ticker ----------------------------------------------------------
// The store's `countdown` is a Vue computed; once cached it only
// re-evaluates when `graceUntil` changes, not when wall-clock time
// moves. We force a re-read once a second by ticking a local ref that
// the countdown formatter depends on. The interval is started lazily
// on the first render and stopped as soon as the banner is hidden so
// idle tenants don't burn a timer.
const tick = ref(0)
let intervalId: ReturnType<typeof setInterval> | null = null

function startTicker(): void {
  if (intervalId !== null) return
  intervalId = setInterval(() => {
    tick.value++
  }, 1000)
}

function stopTicker(): void {
  if (intervalId === null) return
  clearInterval(intervalId)
  intervalId = null
}

watch(
  visible,
  (isVisible) => {
    if (isVisible) startTicker()
    else stopTicker()
  },
  { immediate: true }
)

onBeforeUnmount(stopTicker)

// formatCountdown converts `store.graceUntil` (an RFC3339 string) into
// a "Xd Yh Zm Ws" string. Reading `tick.value` inside the formatter is
// what forces Vue to re-evaluate the enclosing computed every second
// — the store's `countdown` would otherwise stay cached.
function formatCountdown(): string {
  // Touch tick so callers that go through this function re-evaluate
  // every interval. Without it, the `message` computed would only
  // re-run when bannerState changes.
  void tick.value
  const raw = store.graceUntil
  if (!raw) return ''
  const graceMs = Date.parse(raw)
  if (!Number.isFinite(graceMs)) return ''
  const remaining = graceMs - Date.now()
  if (remaining <= 0) return 'grace expired'
  const totalSeconds = Math.floor(remaining / 1000)
  const days = Math.floor(totalSeconds / 86400)
  const hours = Math.floor((totalSeconds % 86400) / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  return `${days}d ${hours}h ${minutes}m ${seconds}s`
}

// formatPeriodEnd renders the subscription period end (used in the
// "canceling-amber" message) in a locale-friendly form. We fall back
// to the raw server string if parsing fails so the user still sees
// something rather than "Invalid Date".
function formatPeriodEnd(raw: string): string {
  if (!raw) return ''
  const ms = Date.parse(raw)
  if (!Number.isFinite(ms)) return raw
  return new Date(ms).toLocaleDateString('en-US', {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  })
}

// ---- CTA -------------------------------------------------------------
// Both "Update Payment" and "Resubscribe" open the Stripe Customer
// Portal — the SPA never collects card details. `returnTo` is the
// current path so Stripe bounces the user back to wherever they were.
const ctaBusy = ref(false)

async function openPortal(): Promise<void> {
  if (ctaBusy.value) return
  ctaBusy.value = true
  try {
    const url = await store.openCustomerPortal(window.location.pathname)
    window.location.href = url
  } catch (err) {
    // Mirrors the store's "swallow into error" approach: a failed
    // handoff to Stripe shouldn't crash the SPA. The user can retry
    // by clicking the button again.
    ctaBusy.value = false
    console.error('Failed to open customer portal:', err)
  }
}

// ---- variant config --------------------------------------------------
// One switch arm per BannerState, exhaustive by construction. Adding
// a new state is a one-place change: the type union flags the new arm
// and the existing ones stay identical.
interface Variant {
  container: string
  cta: string
  ctaLabel: string
}

const variant = computed<Variant | null>(() => {
  switch (store.bannerState) {
    case 'grace-amber':
      return {
        container: 'border-warning/40 bg-warning/5 text-on-surface',
        cta: 'bg-warning text-on-warning hover:bg-warning/90',
        ctaLabel: 'Update Payment',
      }
    case 'expired-red':
      return {
        container: 'border-error/40 bg-error/10 text-on-surface',
        cta: 'bg-error text-on-error hover:bg-error/90',
        ctaLabel: 'Update Payment',
      }
    case 'canceling-amber':
      return {
        container: 'border-warning/40 bg-warning/5 text-on-surface',
        cta: 'bg-warning text-on-warning hover:bg-warning/90',
        ctaLabel: 'Resubscribe',
      }
    case 'downgraded-info':
      return {
        container: 'border-blue-200 bg-blue-50 text-on-surface',
        cta: 'bg-primary text-on-primary hover:bg-primary/90',
        ctaLabel: 'Resubscribe',
      }
    case 'none':
      return null
  }
})

// message is the rendered string for the current state. Pulled out
// of the template so the wording (and the future i18n story) lives
// in one place per state.
const message = computed(() => {
  switch (store.bannerState) {
    case 'grace-amber':
      return `Payment past due — ${formatCountdown()}. Update within grace.`
    case 'expired-red':
      return 'Subscription suspended. Developer limits now in effect.'
    case 'canceling-amber':
      return `Plan ends ${formatPeriodEnd(store.currentPeriodEnd)}. Will downgrade to Developer.`
    case 'downgraded-info':
      return 'On Developer plan — 1 user, 1 SA, 1 connection.'
    case 'none':
      return ''
  }
})

// dismissible mirrors the two soft states. Kept as a computed so the
// template can `v-if` on it without re-deriving the rule inline.
const dismissible = computed(
  (): boolean =>
    store.bannerState === 'canceling-amber' ||
    store.bannerState === 'downgraded-info'
)

// Narrow the imported union into a guard for the template — `variant`
// is `Variant | null` and `message` is `string`, but the template
// also wants to be sure the state is one of the four visible ones
// before dereferencing. `state` lets us do that without `as` casts.
const state = computed<BannerState>(() => store.bannerState)
</script>

<template>
  <div
    v-if="visible && variant && state !== 'none'"
    role="status"
    aria-live="polite"
    :class="['w-full border-b', variant.container]"
    data-testid="billing-banner"
  >
    <div
      class="mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-2 text-sm"
    >
      <p class="flex-1" data-testid="billing-banner-message">{{ message }}</p>
      <div class="flex items-center gap-2">
        <button
          type="button"
          :class="[
            'inline-flex shrink-0 items-center rounded px-3 py-1 font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:cursor-not-allowed disabled:opacity-50',
            variant.cta,
          ]"
          :disabled="ctaBusy"
          data-testid="billing-banner-cta"
          @click="openPortal"
        >
          {{ variant.ctaLabel }}
        </button>
        <button
          v-if="dismissible"
          type="button"
          class="inline-flex shrink-0 items-center rounded p-1 text-on-surface-variant transition-colors hover:bg-surface-variant hover:text-on-surface focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
          aria-label="Dismiss"
          data-testid="billing-banner-dismiss"
          @click="dismissed = true"
        >
          <X :size="16" aria-hidden="true" />
        </button>
      </div>
    </div>
  </div>
</template>
