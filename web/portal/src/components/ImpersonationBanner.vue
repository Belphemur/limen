<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useSessionStore } from '@limen/shared/session'
import { LogOut } from '@lucide/vue'

const session = useSessionStore()
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | null = null

function tenantPrefix(): string {
  const match = window.location.pathname.match(/^(\/t\/[^/]+)\//)
  return match ? match[1] : ''
}

// Compute remaining seconds
const remainingSeconds = computed(() => {
  if (!session.impersonation?.expiresAt) return 0
  const expiresMs = new Date(session.impersonation.expiresAt).getTime()
  return Math.max(0, Math.floor((expiresMs - now.value) / 1000))
})

// Format as MM:SS or HH:MM:SS
const countdown = computed(() => {
  const s = remainingSeconds.value
  const hrs = Math.floor(s / 3600)
  const mins = Math.floor((s % 3600) / 60)
  const secs = s % 60
  const pad = (n: number) => String(n).padStart(2, '0')
  if (s < 300) {
    // Under 5 minutes: show "4m 32s"
    return `${mins}m ${pad(secs)}s`
  }
  if (hrs > 0) {
    return `${hrs}:${pad(mins)}:${pad(secs)}`
  }
  return `${pad(mins)}:${pad(secs)}`
})

// Urgent style when under 5 minutes
const urgent = computed(() => remainingSeconds.value < 300)

// Target name display
const targetName = computed(() => {
  const u = session.user
  if (!u) return 'unknown'
  return `${u.firstName} ${u.lastName}`
})

const targetLabel = computed(() => {
  if (session.impersonation?.targetUserType === 'service_account') {
    return ' (Service Account)'
  }
  return ''
})

const actorName = computed(() => {
  const i = session.impersonation
  if (!i) return 'unknown'
  return `${i.actorFirstName} ${i.actorLastName}`
})

async function endImpersonation() {
  try {
    const prefix = tenantPrefix()
    const resp = await fetch(`${prefix}/api/limen.admin.v1.AdminService/ExitImpersonation`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
      credentials: 'include',
    })
    if (!resp.ok) {
      throw new Error(`ExitImpersonation failed: ${resp.status}`)
    }
  } finally {
    // Redirect to admin panel regardless of success/failure
    window.location.href = `${tenantPrefix()}/admin/`
  }
}

// Effect 1: Start countdown timer
watch(
  () => session.impersonation?.isImpersonating,
  (active) => {
    if (active) {
      now.value = Date.now()
      timer = setInterval(() => {
        now.value = Date.now()
      }, 1000)
    } else {
      if (timer) {
        clearInterval(timer)
        timer = null
      }
    }
  },
  { immediate: true },
)

// Effect 2: Auto-redirect when countdown hits zero
watch(remainingSeconds, (s) => {
  if (s <= 0 && session.impersonation?.isImpersonating) {
    window.location.href = `${tenantPrefix()}/admin/`
  }
})

onBeforeUnmount(() => {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
})
</script>

<template>
  <div
    v-if="session.impersonation?.isImpersonating"
    class="sticky top-0 z-banner flex items-center justify-between gap-4 px-4 py-2.5 text-sm font-medium"
    :class="urgent ? 'bg-red-700 text-white' : 'bg-red-600 text-white'"
  >
    <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
      <span>
        You are viewing as
        <strong>{{ targetName }}</strong>
        ({{ session.user?.email }}){{ targetLabel }}. Impersonated by
        <strong>{{ actorName }}</strong>
        ({{ session.impersonation?.actorEmail }}).
      </span>
      <span
        class="inline-flex items-center rounded bg-white/20 px-2 py-0.5 text-xs font-mono"
        :class="{ 'animate-pulse': urgent }"
      >
        {{ countdown }}
      </span>
    </div>
    <button
      type="button"
      class="flex shrink-0 cursor-pointer items-center gap-1.5 rounded bg-white/20 px-3 py-1 text-xs font-semibold transition-colors hover:bg-white/30 focus:outline-none focus-visible:ring-2 focus-visible:ring-white/50"
      @click="endImpersonation"
    >
      <LogOut :size="14" aria-hidden="true" />
      End impersonation
    </button>
  </div>
</template>
